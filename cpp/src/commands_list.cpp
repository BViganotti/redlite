#include "server.hpp"

#include <algorithm>
#include <chrono>
#include <cmath>
#include <limits>

namespace redlite {

namespace {
using Args = std::vector<std::string>;

void reg(CommandMap& table, const std::string& name, int arity, uint32_t flags,
         std::function<CommandResult(Client&, Args&)> handler) {
  table[name] = Command{name, arity, flags, std::move(handler)};
}
CommandResult res(Reply reply, bool mutated = false, std::vector<std::string> rewrite = {}) {
  CommandResult out;
  out.reply = std::move(reply);
  out.mutated = mutated;
  out.rewrite = std::move(rewrite);
  return out;
}
Reply wrongtype() {
  return Reply::error("WRONGTYPE Operation against a key holding the wrong kind of value");
}

bool parse_direction(const std::string& value) {
  std::string dir = upper_ascii(value);
  if (dir == "LEFT") return true;
  if (dir == "RIGHT") return false;
  throw RedisError("ERR syntax error");
}

int64_t block_timeout_ms(const std::string& value) {
  double seconds = parse_float(value);
  if (seconds < 0) throw RedisError("ERR timeout is negative");
  if (seconds == 0) return 0;
  double ms = seconds * 1000.0;
  if (ms > 1e12) throw RedisError("ERR timeout is not a float or out of range");
  return static_cast<int64_t>(ms);
}

// Waits until signal_seq changes or the deadline expires. The caller must not
// hold the store lock.
void wait_signal(Client& client, uint64_t seq, std::chrono::steady_clock::time_point deadline,
                 bool infinite) {
  std::unique_lock<std::mutex> lock(client.srv.store.wait_mu);
  auto predicate = [&] {
    return client.srv.store.signal_seq.load() != seq || client.srv.shutting_down.load();
  };
  if (infinite) {
    client.srv.store.cv.wait(lock, predicate);
  } else {
    client.srv.store.cv.wait_until(lock, deadline, predicate);
  }
}


CommandResult blocking_pop(Client& client, Args& args, bool left) {
  int64_t timeout_ms = block_timeout_ms(args.back());
  std::vector<std::string> keys(args.begin() + 1, args.end() - 1);
  bool infinite = timeout_ms == 0;
  auto deadline = std::chrono::steady_clock::now() + std::chrono::milliseconds(timeout_ms);
  for (;;) {
    uint64_t seq;
    {
      std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
      seq = client.srv.store.signal_seq.load();
      for (const auto& key : keys) {
        Entry* entry = client.srv.store.lookup_write(key, client.srv.store.now_ms());
        if (!entry) continue;
        if (value_type(entry->value) != ValueType::List) return res(wrongtype());
        List& list = std::get<List>(entry->value);
        if (list.empty()) continue;
        std::string value = left ? list.front() : list.back();
        if (left) list.pop_front();
        else list.pop_back();
        if (list.empty()) client.srv.store.data.erase(key);
        else client.srv.store.touch(*entry);
        client.srv.store.signal();
        return res(Reply::array({Reply::bulk(key), Reply::bulk(value)}), true,
                   {left ? "LPOP" : "RPOP", key});
      }
    }
    if (client.executing || client.srv.shutting_down.load()) return res(Reply::null_array());
    wait_signal(client, seq, deadline, infinite);
    if (!infinite && std::chrono::steady_clock::now() >= deadline) return res(Reply::null_array());
  }
}

CommandResult blocking_move(Client& client, const std::string& source, const std::string& dest,
                            bool from_left, bool to_left, int64_t timeout_ms) {
  bool infinite = timeout_ms == 0;
  auto deadline = std::chrono::steady_clock::now() + std::chrono::milliseconds(timeout_ms);
  for (;;) {
    uint64_t seq;
    {
      std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
      seq = client.srv.store.signal_seq.load();
      Entry* src = client.srv.store.lookup_write(source, client.srv.store.now_ms());
      Entry* dst = client.srv.store.lookup_write(dest, client.srv.store.now_ms());
      if (src && value_type(src->value) != ValueType::List) return res(wrongtype());
      if (dst && value_type(dst->value) != ValueType::List) return res(wrongtype());
      if (src) {
        List& list = std::get<List>(src->value);
        if (!list.empty()) {
          std::string value = from_left ? list.front() : list.back();
          if (from_left) list.pop_front();
          else list.pop_back();
          if (source == dest) {
            if (to_left) list.push_front(value);
            else list.push_back(value);
            client.srv.store.touch(*src);
          } else {
            if (list.empty()) client.srv.store.data.erase(source);
            else client.srv.store.touch(*src);
            if (!dst) {
              dst = &client.srv.store.create(dest);
              dst->value = List{};
            }
            List& target = std::get<List>(dst->value);
            if (to_left) target.push_front(value);
            else target.push_back(value);
            client.srv.store.touch(*dst);
          }
          client.srv.store.signal();
          return res(Reply::bulk(value), true,
                     {"LMOVE", source, dest, from_left ? "LEFT" : "RIGHT",
                      to_left ? "LEFT" : "RIGHT"});
        }
      }
    }
    if (client.executing || client.srv.shutting_down.load()) return res(Reply::null());
    wait_signal(client, seq, deadline, infinite);
    if (!infinite && std::chrono::steady_clock::now() >= deadline) return res(Reply::null());
  }
}

CommandResult lmove(Client& client, const std::string& source, const std::string& dest,
                    bool from_left, bool to_left) {
  std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
  int64_t now = client.srv.store.now_ms();
  Entry* src = client.srv.store.lookup_write(source, now);
  if (!src) return res(Reply::null());
  if (value_type(src->value) != ValueType::List) return res(wrongtype());
  List& list = std::get<List>(src->value);
  if (list.empty()) return res(Reply::null());
  if (source == dest) {
    std::string value = from_left ? list.front() : list.back();
    if (from_left) list.pop_front();
    else list.pop_back();
    if (to_left) list.push_front(value);
    else list.push_back(value);
    client.srv.store.touch(*src);
    client.srv.store.signal();
    return res(Reply::bulk(value), true);
  }
  Entry* dst = client.srv.store.lookup_write(dest, now);
  if (dst && value_type(dst->value) != ValueType::List) return res(wrongtype());
  std::string value = from_left ? list.front() : list.back();
  if (from_left) list.pop_front();
  else list.pop_back();
  if (list.empty()) client.srv.store.data.erase(source);
  else client.srv.store.touch(*src);
  if (!dst) {
    dst = &client.srv.store.create(dest);
    dst->value = List{};
  }
  List& target = std::get<List>(dst->value);
  if (to_left) target.push_front(value);
  else target.push_back(value);
  client.srv.store.touch(*dst);
  client.srv.store.signal();
  return res(Reply::bulk(value), true,
             {"LMOVE", source, dest, from_left ? "LEFT" : "RIGHT",
              to_left ? "LEFT" : "RIGHT"});
}
}  // namespace

void register_list_commands(CommandMap& table) {
  auto push = [](Client& client, Args& args, bool front, bool create) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    Entry* entry = client.srv.store.lookup_write(args[1], client.srv.store.now_ms());
    if (!entry) {
      if (!create) return res(Reply::integer_reply(0));
      entry = &client.srv.store.create(args[1]);
      entry->value = List{};
    }
    if (value_type(entry->value) != ValueType::List) return res(wrongtype());
    List& list = std::get<List>(entry->value);
    for (size_t i = 2; i < args.size(); ++i) {
      if (front) list.push_front(args[i]);
      else list.push_back(args[i]);
    }
    client.srv.store.touch(*entry);
    client.srv.store.signal();
    return res(Reply::integer_reply(static_cast<int64_t>(list.size())), true);
  };
  reg(table, "LPUSH", -3, kWrite, [push](Client& c, Args& a) { return push(c, a, true, true); });
  reg(table, "RPUSH", -3, kWrite, [push](Client& c, Args& a) { return push(c, a, false, true); });
  reg(table, "LPUSHX", -3, kWrite, [push](Client& c, Args& a) { return push(c, a, true, false); });
  reg(table, "RPUSHX", -3, kWrite, [push](Client& c, Args& a) { return push(c, a, false, false); });

  auto pop = [](Client& client, Args& args, bool left) -> CommandResult {
    int64_t count = 1;
    bool with_count = args.size() == 3;
    if (args.size() > 3) return res(Reply::error("ERR wrong number of arguments for '" + lower_ascii(args[0]) + "' command"));
    if (with_count) {
      count = parse_int(args[2]);
      if (count < 0) return res(Reply::error("ERR value is out of range, must be positive"));
    }
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    Entry* entry = client.srv.store.lookup_write(args[1], client.srv.store.now_ms());
    if (!entry) {
      return res(with_count ? Reply::array({}) : Reply::null());
    }
    if (value_type(entry->value) != ValueType::List) return res(wrongtype());
    List& list = std::get<List>(entry->value);
    std::vector<std::string> values;
    for (int64_t i = 0; i < count && !list.empty(); ++i) {
      values.push_back(left ? list.front() : list.back());
      if (left) list.pop_front();
      else list.pop_back();
    }
    if (list.empty()) client.srv.store.data.erase(args[1]);
    else if (!values.empty()) client.srv.store.touch(*entry);
    if (!values.empty()) client.srv.store.signal();
    if (!with_count) {
      if (values.empty()) return res(Reply::null());
      return res(Reply::bulk(values.front()), !values.empty(),
                 {left ? "LPOP" : "RPOP", args[1]});
    }
    std::vector<Reply> out;
    for (const auto& value : values) out.push_back(Reply::bulk(value));
    return res(Reply::array(std::move(out)), !values.empty(),
               {left ? "LPOP" : "RPOP", args[1], std::to_string(values.size())});
  };
  reg(table, "LPOP", -2, kWrite | kFast, [pop](Client& c, Args& a) { return pop(c, a, true); });
  reg(table, "RPOP", -2, kWrite | kFast, [pop](Client& c, Args& a) { return pop(c, a, false); });

  reg(table, "LLEN", 2, kReadOnly | kFast, [](Client& client, Args& args) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    const Entry* entry = client.srv.store.lookup_read(args[1], client.srv.store.now_ms());
    if (!entry) return res(Reply::integer_reply(0));
    if (value_type(entry->value) != ValueType::List) return res(wrongtype());
    return res(Reply::integer_reply(static_cast<int64_t>(std::get<List>(entry->value).size())));
  });

  reg(table, "LINDEX", 3, kReadOnly | kFast, [](Client& client, Args& args) -> CommandResult {
    int64_t index = parse_int(args[2]);
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    const Entry* entry = client.srv.store.lookup_read(args[1], client.srv.store.now_ms());
    if (!entry) return res(Reply::null());
    if (value_type(entry->value) != ValueType::List) return res(wrongtype());
    const List& list = std::get<List>(entry->value);
    int64_t size = static_cast<int64_t>(list.size());
    if (index < 0) index += size;
    if (index < 0 || index >= size) return res(Reply::null());
    return res(Reply::bulk(list[static_cast<size_t>(index)]));
  });

  reg(table, "LSET", 4, kWrite, [](Client& client, Args& args) -> CommandResult {
    int64_t index = parse_int(args[2]);
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    Entry* entry = client.srv.store.lookup_write(args[1], client.srv.store.now_ms());
    if (!entry) return res(Reply::error("ERR no such key"));
    if (value_type(entry->value) != ValueType::List) return res(wrongtype());
    List& list = std::get<List>(entry->value);
    int64_t size = static_cast<int64_t>(list.size());
    if (index < 0) index += size;
    if (index < 0 || index >= size) return res(Reply::error("ERR index out of range"));
    list[static_cast<size_t>(index)] = args[3];
    client.srv.store.touch(*entry);
    return res(Reply::simple("OK"), true);
  });

  reg(table, "LRANGE", 4, kReadOnly, [](Client& client, Args& args) -> CommandResult {
    int64_t start = parse_int(args[2]);
    int64_t stop = parse_int(args[3]);
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    const Entry* entry = client.srv.store.lookup_read(args[1], client.srv.store.now_ms());
    if (!entry) return res(Reply::array({}));
    if (value_type(entry->value) != ValueType::List) return res(wrongtype());
    const List& list = std::get<List>(entry->value);
    int64_t size = static_cast<int64_t>(list.size());
    if (start < 0) start += size;
    if (stop < 0) stop += size;
    if (start < 0) start = 0;
    if (stop >= size) stop = size - 1;
    if (start > stop || start >= size) return res(Reply::array({}));
    std::vector<Reply> out;
    for (int64_t i = start; i <= stop; ++i) out.push_back(Reply::bulk(list[static_cast<size_t>(i)]));
    return res(Reply::array(std::move(out)));
  });

  reg(table, "LTRIM", 4, kWrite, [](Client& client, Args& args) -> CommandResult {
    int64_t start = parse_int(args[2]);
    int64_t stop = parse_int(args[3]);
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    Entry* entry = client.srv.store.lookup_write(args[1], client.srv.store.now_ms());
    if (!entry) return res(Reply::simple("OK"));
    if (value_type(entry->value) != ValueType::List) return res(wrongtype());
    List& list = std::get<List>(entry->value);
    int64_t size = static_cast<int64_t>(list.size());
    if (start < 0) start += size;
    if (stop < 0) stop += size;
    if (start < 0) start = 0;
    if (stop >= size) stop = size - 1;
    List trimmed;
    for (int64_t i = start; i <= stop && i < size; ++i) {
      if (i >= 0) trimmed.push_back(list[static_cast<size_t>(i)]);
    }
    if (trimmed.empty()) client.srv.store.data.erase(args[1]);
    else {
      list = std::move(trimmed);
      client.srv.store.touch(*entry);
    }
    return res(Reply::simple("OK"), true);
  });

  reg(table, "LREM", 4, kWrite, [](Client& client, Args& args) -> CommandResult {
    int64_t count = parse_int(args[2]);
    const std::string& target = args[3];
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    Entry* entry = client.srv.store.lookup_write(args[1], client.srv.store.now_ms());
    if (!entry) return res(Reply::integer_reply(0));
    if (value_type(entry->value) != ValueType::List) return res(wrongtype());
    List& list = std::get<List>(entry->value);
    List kept;
    int64_t removed = 0;
    if (count >= 0) {
      int64_t limit = count == 0 ? std::numeric_limits<int64_t>::max() : count;
      for (const auto& value : list) {
        if (removed < limit && value == target) ++removed;
        else kept.push_back(value);
      }
    } else {
      int64_t limit = -count;
      std::vector<bool> remove(list.size(), false);
      for (int64_t i = static_cast<int64_t>(list.size()) - 1; i >= 0 && removed < limit; --i) {
        if (list[static_cast<size_t>(i)] == target) {
          remove[static_cast<size_t>(i)] = true;
          ++removed;
        }
      }
      for (size_t i = 0; i < list.size(); ++i) {
        if (!remove[i]) kept.push_back(list[i]);
      }
    }
    if (kept.empty()) client.srv.store.data.erase(args[1]);
    else if (removed > 0) {
      list = std::move(kept);
      client.srv.store.touch(*entry);
    }
    return res(Reply::integer_reply(removed), removed > 0);
  });

  reg(table, "LINSERT", 5, kWrite, [](Client& client, Args& args) -> CommandResult {
    std::string where = upper_ascii(args[2]);
    bool before;
    if (where == "BEFORE") before = true;
    else if (where == "AFTER") before = false;
    else return res(Reply::error("ERR syntax error"));
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    Entry* entry = client.srv.store.lookup_write(args[1], client.srv.store.now_ms());
    if (!entry) return res(Reply::integer_reply(0));
    if (value_type(entry->value) != ValueType::List) return res(wrongtype());
    List& list = std::get<List>(entry->value);
    auto it = std::find(list.begin(), list.end(), args[3]);
    if (it == list.end()) return res(Reply::integer_reply(-1));
    if (before) list.insert(it, args[4]);
    else list.insert(std::next(it), args[4]);
    client.srv.store.touch(*entry);
    return res(Reply::integer_reply(static_cast<int64_t>(list.size())), true);
  });

  reg(table, "LPOS", -3, kReadOnly, [](Client& client, Args& args) -> CommandResult {
    const std::string& element = args[2];
    int64_t rank = 1;
    int64_t count = -1;
    int64_t maxlen = 0;
    for (size_t i = 3; i + 1 < args.size(); i += 2) {
      std::string option = upper_ascii(args[i]);
      int64_t value = parse_int(args[i + 1]);
      if (option == "RANK") {
        if (value == 0) return res(Reply::error("ERR RANK can't be zero"));
        rank = value;
      } else if (option == "COUNT") {
        if (value < 0) return res(Reply::error("ERR COUNT can't be negative"));
        count = value;
      } else if (option == "MAXLEN") {
        if (value < 0) return res(Reply::error("ERR MAXLEN can't be negative"));
        maxlen = value;
      } else {
        return res(Reply::error("ERR syntax error"));
      }
    }
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    const Entry* entry = client.srv.store.lookup_read(args[1], client.srv.store.now_ms());
    if (!entry) return res(count == -1 ? Reply::null() : Reply::array({}));
    if (value_type(entry->value) != ValueType::List) return res(wrongtype());
    const List& list = std::get<List>(entry->value);
    std::vector<int64_t> positions;
    if (rank >= 0) {
      int64_t matched = 0;
      for (size_t i = 0; i < list.size(); ++i) {
        if (maxlen > 0 && static_cast<int64_t>(i) >= maxlen) break;
        if (list[i] != element) continue;
        ++matched;
        if (matched < rank) continue;
        positions.push_back(static_cast<int64_t>(i));
        if (count > 0 && static_cast<int64_t>(positions.size()) >= count) break;
      }
    } else {
      int64_t matched = 0, examined = 0;
      for (int64_t i = static_cast<int64_t>(list.size()) - 1; i >= 0; --i) {
        ++examined;
        if (maxlen > 0 && examined > maxlen) break;
        if (list[static_cast<size_t>(i)] != element) continue;
        ++matched;
        if (matched < -rank) continue;
        positions.push_back(i);
        if (count > 0 && static_cast<int64_t>(positions.size()) >= count) break;
      }
    }
    if (count == -1) {
      if (positions.empty()) return res(Reply::null());
      return res(Reply::integer_reply(positions.front()));
    }
    std::vector<Reply> out;
    for (int64_t position : positions) out.push_back(Reply::integer_reply(position));
    return res(Reply::array(std::move(out)));
  });

  reg(table, "LMOVE", 5, kWrite, [](Client& client, Args& args) -> CommandResult {
    bool from_left = parse_direction(args[3]);
    bool to_left = parse_direction(args[4]);
    return lmove(client, args[1], args[2], from_left, to_left);
  });
  reg(table, "RPOPLPUSH", 3, kWrite, [](Client& client, Args& args) -> CommandResult {
    return lmove(client, args[1], args[2], false, true);
  });
  reg(table, "BLPOP", -3, kWrite | kBlocking, [](Client& client, Args& args) -> CommandResult {
    return blocking_pop(client, args, true);
  });
  reg(table, "BRPOP", -3, kWrite | kBlocking, [](Client& client, Args& args) -> CommandResult {
    return blocking_pop(client, args, false);
  });
  reg(table, "BLMOVE", 6, kWrite | kBlocking, [](Client& client, Args& args) -> CommandResult {
    bool from_left = parse_direction(args[3]);
    bool to_left = parse_direction(args[4]);
    int64_t timeout = block_timeout_ms(args[5]);
    return blocking_move(client, args[1], args[2], from_left, to_left, timeout);
  });
  reg(table, "BRPOPLPUSH", 4, kWrite | kBlocking, [](Client& client, Args& args) -> CommandResult {
    int64_t timeout = block_timeout_ms(args[3]);
    return blocking_move(client, args[1], args[2], false, true, timeout);
  });

  reg(table, "LMPOP", -4, kWrite, [](Client& client, Args& args) -> CommandResult {
    int64_t numkeys = parse_int(args[1]);
    if (numkeys <= 0) return res(Reply::error("ERR numkeys should be greater than 0"));
    if (static_cast<size_t>(numkeys) > args.size() - 2) return res(Reply::error("ERR Number of keys can't be greater than number of args"));
    std::vector<std::string> keys(args.begin() + 2, args.begin() + 2 + numkeys);
    size_t index = 2 + static_cast<size_t>(numkeys);
    if (index >= args.size()) return res(Reply::error("ERR syntax error"));
    bool left;
    std::string dir = upper_ascii(args[index]);
    if (dir == "LEFT") left = true;
    else if (dir == "RIGHT") left = false;
    else return res(Reply::error("ERR syntax error"));
    int64_t count = 1;
    if (++index < args.size()) {
      if (upper_ascii(args[index]) != "COUNT" || index + 1 >= args.size()) return res(Reply::error("ERR syntax error"));
      count = parse_int(args[++index]);
      if (count <= 0) return res(Reply::error("ERR count should be greater than 0"));
    }
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    for (const auto& key : keys) {
      Entry* entry = client.srv.store.lookup_write(key, client.srv.store.now_ms());
      if (!entry) continue;
      if (value_type(entry->value) != ValueType::List) return res(wrongtype());
      List& list = std::get<List>(entry->value);
      if (list.empty()) continue;
      std::vector<Reply> values;
      for (int64_t i = 0; i < count && !list.empty(); ++i) {
        values.push_back(Reply::bulk(left ? list.front() : list.back()));
        if (left) list.pop_front();
        else list.pop_back();
      }
      if (list.empty()) client.srv.store.data.erase(key);
      else client.srv.store.touch(*entry);
      client.srv.store.signal();
      return res(Reply::array({Reply::bulk(key), Reply::array(std::move(values))}), true,
                 {left ? "LPOP" : "RPOP", key, std::to_string(count)});
    }
    return res(Reply::null_array());
  });
}

}  // namespace redlite
