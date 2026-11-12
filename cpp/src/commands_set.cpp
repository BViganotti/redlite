#include "server.hpp"

#include <algorithm>

namespace redlite {

namespace {
using Args = std::vector<std::string>;
using SetValue = std::unordered_set<std::string>;

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

std::vector<Reply> bulk_list(const std::vector<std::string>& values) {
  std::vector<Reply> out;
  for (const auto& value : values) out.push_back(Reply::bulk(value));
  return out;
}
}  // namespace

void register_set_commands(CommandMap& table) {
  reg(table, "SADD", -3, kWrite, [](Client& client, Args& args) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    Entry* entry = client.srv.store.lookup_write(args[1], client.srv.store.now_ms());
    if (!entry) {
      entry = &client.srv.store.create(args[1]);
      entry->value = Set{};
    }
    if (value_type(entry->value) != ValueType::Set) return res(wrongtype());
    Set& set = std::get<Set>(entry->value);
    int64_t added = 0;
    for (size_t i = 2; i < args.size(); ++i) added += set.insert(args[i]).second ? 1 : 0;
    if (added > 0) client.srv.store.touch(*entry);
    return res(Reply::integer_reply(added), added > 0);
  });
  reg(table, "SREM", -3, kWrite | kFast, [](Client& client, Args& args) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    Entry* entry = client.srv.store.lookup_write(args[1], client.srv.store.now_ms());
    if (!entry) return res(Reply::integer_reply(0));
    if (value_type(entry->value) != ValueType::Set) return res(wrongtype());
    Set& set = std::get<Set>(entry->value);
    int64_t removed = 0;
    for (size_t i = 2; i < args.size(); ++i) removed += set.erase(args[i]);
    if (set.empty()) client.srv.store.data.erase(args[1]);
    else if (removed > 0) client.srv.store.touch(*entry);
    return res(Reply::integer_reply(removed), removed > 0);
  });
  reg(table, "SCARD", 2, kReadOnly | kFast, [](Client& client, Args& args) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    const Entry* entry = client.srv.store.lookup_read(args[1], client.srv.store.now_ms());
    if (!entry) return res(Reply::integer_reply(0));
    if (value_type(entry->value) != ValueType::Set) return res(wrongtype());
    return res(Reply::integer_reply(static_cast<int64_t>(std::get<Set>(entry->value).size())));
  });
  reg(table, "SISMEMBER", 3, kReadOnly | kFast, [](Client& client, Args& args) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    const Entry* entry = client.srv.store.lookup_read(args[1], client.srv.store.now_ms());
    if (!entry) return res(Reply::integer_reply(0));
    if (value_type(entry->value) != ValueType::Set) return res(wrongtype());
    return res(Reply::integer_reply(std::get<Set>(entry->value).count(args[2]) ? 1 : 0));
  });
  reg(table, "SMISMEMBER", -3, kReadOnly | kFast, [](Client& client, Args& args) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    const Entry* entry = client.srv.store.lookup_read(args[1], client.srv.store.now_ms());
    if (entry && value_type(entry->value) != ValueType::Set) return res(wrongtype());
    const Set* set = entry ? &std::get<Set>(entry->value) : nullptr;
    std::vector<Reply> out;
    for (size_t i = 2; i < args.size(); ++i) {
      out.push_back(Reply::integer_reply(set && set->count(args[i]) ? 1 : 0));
    }
    return res(Reply::array(std::move(out)));
  });
  reg(table, "SMEMBERS", 2, kReadOnly, [](Client& client, Args& args) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    const Entry* entry = client.srv.store.lookup_read(args[1], client.srv.store.now_ms());
    if (!entry) return res(Reply::set({}));
    if (value_type(entry->value) != ValueType::Set) return res(wrongtype());
    return res(Reply::set(bulk_list(sorted_members(std::get<Set>(entry->value)))));
  });

  auto algebra = [](Client& client, Args& args, int op) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    int64_t now = client.srv.store.now_ms();
    std::vector<std::unordered_set<std::string>> sets;
    for (size_t i = 1; i < args.size(); ++i) {
      const Entry* entry = client.srv.store.lookup_read(args[i], now);
      if (!entry) {
        sets.emplace_back();
        continue;
      }
      if (value_type(entry->value) != ValueType::Set) return res(wrongtype());
      sets.push_back(std::get<Set>(entry->value));
    }
    std::unordered_set<std::string> result;
    if (op == 0) {
      if (!sets.empty()) {
        result = sets[0];
        for (size_t i = 1; i < sets.size(); ++i) {
          for (auto it = result.begin(); it != result.end();) {
            if (!sets[i].count(*it)) it = result.erase(it);
            else ++it;
          }
        }
      }
    } else if (op == 1) {
      for (const auto& set : sets) result.insert(set.begin(), set.end());
    } else {
      if (!sets.empty()) {
        result = sets[0];
        for (size_t i = 1; i < sets.size(); ++i) {
          for (const auto& member : sets[i]) result.erase(member);
        }
      }
    }
    std::vector<std::string> members(result.begin(), result.end());
    std::sort(members.begin(), members.end());
    return res(Reply::set(bulk_list(members)));
  };
  reg(table, "SINTER", -2, kReadOnly, [algebra](Client& c, Args& a) { return algebra(c, a, 0); });
  reg(table, "SUNION", -2, kReadOnly, [algebra](Client& c, Args& a) { return algebra(c, a, 1); });
  reg(table, "SDIFF", -2, kReadOnly, [algebra](Client& c, Args& a) { return algebra(c, a, 2); });

  auto store_algebra = [](Client& client, Args& args, int op) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    int64_t now = client.srv.store.now_ms();
    std::vector<std::unordered_set<std::string>> sets;
    for (size_t i = 2; i < args.size(); ++i) {
      const Entry* entry = client.srv.store.lookup_read(args[i], now);
      if (!entry) {
        sets.emplace_back();
        continue;
      }
      if (value_type(entry->value) != ValueType::Set) return res(wrongtype());
      sets.push_back(std::get<Set>(entry->value));
    }
    std::unordered_set<std::string> result;
    if (op == 0) {
      if (!sets.empty()) {
        result = sets[0];
        for (size_t i = 1; i < sets.size(); ++i) {
          for (auto it = result.begin(); it != result.end();) {
            if (!sets[i].count(*it)) it = result.erase(it);
            else ++it;
          }
        }
      }
    } else if (op == 1) {
      for (const auto& set : sets) result.insert(set.begin(), set.end());
    } else {
      if (!sets.empty()) {
        result = sets[0];
        for (size_t i = 1; i < sets.size(); ++i) {
          for (const auto& member : sets[i]) result.erase(member);
        }
      }
    }
    if (result.empty()) {
      client.srv.store.data.erase(args[1]);
      return res(Reply::integer_reply(0), true, {"DEL", args[1]});
    }
    Entry* entry = client.srv.store.lookup_write(args[1], now);
    if (!entry) entry = &client.srv.store.create(args[1]);
    entry->value = Set(result.begin(), result.end());
    client.srv.store.touch(*entry);
    return res(Reply::integer_reply(static_cast<int64_t>(result.size())), true);
  };
  reg(table, "SINTERSTORE", -3, kWrite, [store_algebra](Client& c, Args& a) {
    return store_algebra(c, a, 0);
  });
  reg(table, "SUNIONSTORE", -3, kWrite, [store_algebra](Client& c, Args& a) {
    return store_algebra(c, a, 1);
  });
  reg(table, "SDIFFSTORE", -3, kWrite, [store_algebra](Client& c, Args& a) {
    return store_algebra(c, a, 2);
  });

  reg(table, "SMOVE", 4, kWrite | kFast, [](Client& client, Args& args) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    int64_t now = client.srv.store.now_ms();
    Entry* src = client.srv.store.lookup_write(args[1], now);
    if (!src) return res(Reply::integer_reply(0));
    if (value_type(src->value) != ValueType::Set) return res(wrongtype());
    Set& source = std::get<Set>(src->value);
    if (!source.count(args[3])) return res(Reply::integer_reply(0));
    Entry* dst = client.srv.store.lookup_write(args[2], now);
    if (dst && value_type(dst->value) != ValueType::Set) return res(wrongtype());
    if (!dst) {
      dst = &client.srv.store.create(args[2]);
      dst->value = Set{};
    }
    source.erase(args[3]);
    std::get<Set>(dst->value).insert(args[3]);
    client.srv.store.touch(*dst);
    if (source.empty()) client.srv.store.data.erase(args[1]);
    else client.srv.store.touch(*src);
    return res(Reply::integer_reply(1), true);
  });

  reg(table, "SINTERCARD", -3, kReadOnly, [](Client& client, Args& args) -> CommandResult {
    int64_t numkeys = parse_int(args[1]);
    if (numkeys <= 0) return res(Reply::error("ERR numkeys should be greater than 0"));
    if (static_cast<size_t>(numkeys) > args.size() - 2) {
      return res(Reply::error("ERR Number of keys can't be greater than number of args"));
    }
    int64_t limit = 0;
    size_t index = 2 + static_cast<size_t>(numkeys);
    if (index < args.size()) {
      if (upper_ascii(args[index]) != "LIMIT" || index + 1 >= args.size()) return res(Reply::error("ERR syntax error"));
      limit = parse_int(args[index + 1]);
      if (limit < 0) return res(Reply::error("ERR LIMIT can't be negative"));
      index += 2;
      if (index != args.size()) return res(Reply::error("ERR syntax error"));
    }
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    int64_t now = client.srv.store.now_ms();
    std::vector<std::unordered_set<std::string>> sets;
    for (size_t i = 2; i < 2 + static_cast<size_t>(numkeys); ++i) {
      const Entry* entry = client.srv.store.lookup_read(args[i], now);
      if (!entry) return res(Reply::integer_reply(0));
      if (value_type(entry->value) != ValueType::Set) return res(wrongtype());
      sets.push_back(std::get<Set>(entry->value));
    }
    if (sets.empty()) return res(Reply::integer_reply(0));
    size_t smallest = 0;
    for (size_t i = 1; i < sets.size(); ++i) {
      if (sets[i].size() < sets[smallest].size()) smallest = i;
    }
    int64_t count = 0;
    for (const auto& member : sets[smallest]) {
      bool present = true;
      for (size_t i = 0; i < sets.size(); ++i) {
        if (i == smallest) continue;
        if (!sets[i].count(member)) {
          present = false;
          break;
        }
      }
      if (present) {
        ++count;
        if (limit > 0 && count >= limit) break;
      }
    }
    return res(Reply::integer_reply(count));
  });

  auto spop = [](Client& client, Args& args) -> CommandResult {
    int64_t count = 1;
    bool with_count = args.size() == 3;
    if (args.size() > 3) return res(Reply::error("ERR wrong number of arguments for 'spop' command"));
    if (with_count) {
      count = parse_int(args[2]);
      if (count < 0) return res(Reply::error("ERR value is out of range, must be positive"));
    }
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    Entry* entry = client.srv.store.lookup_write(args[1], client.srv.store.now_ms());
    if (!entry) return res(with_count ? Reply::set({}) : Reply::null());
    if (value_type(entry->value) != ValueType::Set) return res(wrongtype());
    Set& set = std::get<Set>(entry->value);
    auto members = sorted_members(set);
    std::vector<std::string> removed;
    for (int64_t i = 0; i < count && i < static_cast<int64_t>(members.size()); ++i) {
      set.erase(members[static_cast<size_t>(i)]);
      removed.push_back(members[static_cast<size_t>(i)]);
    }
    if (set.empty()) client.srv.store.data.erase(args[1]);
    else if (!removed.empty()) client.srv.store.touch(*entry);
    if (!with_count) {
      if (removed.empty()) return res(Reply::null());
      return res(Reply::bulk(removed.front()), true,
                 {"SREM", args[1], removed.front()});
    }
    std::vector<std::string> rewrite = {"SREM", args[1]};
    rewrite.insert(rewrite.end(), removed.begin(), removed.end());
    return res(Reply::set(bulk_list(removed)), !removed.empty(), rewrite);
  };
  reg(table, "SPOP", -2, kWrite | kFast, spop);

  reg(table, "SRANDMEMBER", -2, kReadOnly, [](Client& client, Args& args) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    const Entry* entry = client.srv.store.lookup_read(args[1], client.srv.store.now_ms());
    if (!entry) return res(args.size() == 2 ? Reply::null() : Reply::array({}));
    if (value_type(entry->value) != ValueType::Set) return res(wrongtype());
    auto members = sorted_members(std::get<Set>(entry->value));
    if (args.size() == 2) {
      if (members.empty()) return res(Reply::null());
      return res(Reply::bulk(members.front()));
    }
    int64_t count = parse_int(args[2]);
    std::vector<std::string> out;
    if (count < 0) {
      count = -count;
      for (int64_t i = 0; i < count && !members.empty(); ++i) {
        out.push_back(members[static_cast<size_t>(i) % members.size()]);
      }
    } else {
      for (int64_t i = 0; i < count && i < static_cast<int64_t>(members.size()); ++i) {
        out.push_back(members[static_cast<size_t>(i)]);
      }
    }
    return res(Reply::array(bulk_list(out)));
  });

  reg(table, "SSCAN", -3, kReadOnly, [](Client& client, Args& args) -> CommandResult {
    uint64_t cursor = static_cast<uint64_t>(parse_int(args[2]));
    std::string pattern = "*";
    int64_t count = 10;
    for (size_t i = 3; i + 1 < args.size(); i += 2) {
      std::string option = upper_ascii(args[i]);
      if (option == "MATCH") pattern = args[i + 1];
      else if (option == "COUNT") count = parse_int(args[i + 1]);
      else return res(Reply::error("ERR syntax error"));
    }
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    const Entry* entry = client.srv.store.lookup_read(args[1], client.srv.store.now_ms());
    if (!entry) return res(Reply::array({Reply::bulk("0"), Reply::array({})}));
    if (value_type(entry->value) != ValueType::Set) return res(wrongtype());
    auto members = sorted_members(std::get<Set>(entry->value));
    std::vector<Reply> page;
    size_t examined = 0;
    while (cursor < members.size() && examined < static_cast<size_t>(count)) {
      if (glob_match_local(pattern, members[cursor])) page.push_back(Reply::bulk(members[cursor]));
      ++cursor;
      ++examined;
    }
    if (cursor >= members.size()) cursor = 0;
    return res(Reply::array({Reply::bulk(std::to_string(cursor)), Reply::array(std::move(page))}));
  });
}

}  // namespace redlite
