#include "server.hpp"

#include <algorithm>
#include <cmath>

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
}  // namespace

void register_hash_commands(CommandMap& table) {
  reg(table, "HSET", -4, kWrite, [](Client& client, Args& args) -> CommandResult {
    if ((args.size() - 2) % 2 != 0) {
      return res(Reply::error("ERR wrong number of arguments for 'hset' command"));
    }
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    Entry* entry = client.srv.store.lookup_write(args[1], client.srv.store.now_ms());
    if (!entry) {
      entry = &client.srv.store.create(args[1]);
      entry->value = Hash{};
    }
    if (value_type(entry->value) != ValueType::Hash) return res(wrongtype());
    Hash& hash = std::get<Hash>(entry->value);
    int64_t added = 0;
    for (size_t i = 2; i + 1 < args.size(); i += 2) {
      if (hash.find(args[i]) == hash.end()) ++added;
      hash[args[i]] = args[i + 1];
    }
    client.srv.store.touch(*entry);
    return res(Reply::integer_reply(added), true);
  });
  reg(table, "HMSET", -4, kWrite, [](Client& client, Args& args) -> CommandResult {
    if ((args.size() - 2) % 2 != 0) {
      return res(Reply::error("ERR wrong number of arguments for 'hmset' command"));
    }
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    Entry* entry = client.srv.store.lookup_write(args[1], client.srv.store.now_ms());
    if (!entry) {
      entry = &client.srv.store.create(args[1]);
      entry->value = Hash{};
    }
    if (value_type(entry->value) != ValueType::Hash) return res(wrongtype());
    Hash& hash = std::get<Hash>(entry->value);
    for (size_t i = 2; i + 1 < args.size(); i += 2) hash[args[i]] = args[i + 1];
    client.srv.store.touch(*entry);
    return res(Reply::simple("OK"), true);
  });
  reg(table, "HSETNX", 4, kWrite, [](Client& client, Args& args) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    Entry* entry = client.srv.store.lookup_write(args[1], client.srv.store.now_ms());
    if (!entry) {
      entry = &client.srv.store.create(args[1]);
      entry->value = Hash{};
    }
    if (value_type(entry->value) != ValueType::Hash) return res(wrongtype());
    Hash& hash = std::get<Hash>(entry->value);
    if (hash.find(args[2]) != hash.end()) return res(Reply::integer_reply(0));
    hash[args[2]] = args[3];
    client.srv.store.touch(*entry);
    return res(Reply::integer_reply(1), true);
  });
  reg(table, "HGET", 3, kReadOnly | kFast, [](Client& client, Args& args) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    const Entry* entry = client.srv.store.lookup_read(args[1], client.srv.store.now_ms());
    if (!entry) return res(Reply::null());
    if (value_type(entry->value) != ValueType::Hash) return res(wrongtype());
    const Hash& hash = std::get<Hash>(entry->value);
    auto it = hash.find(args[2]);
    if (it == hash.end()) return res(Reply::null());
    return res(Reply::bulk(it->second));
  });
  reg(table, "HMGET", -3, kReadOnly | kFast, [](Client& client, Args& args) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    const Entry* entry = client.srv.store.lookup_read(args[1], client.srv.store.now_ms());
    if (entry && value_type(entry->value) != ValueType::Hash) return res(wrongtype());
    const Hash* hash = entry ? &std::get<Hash>(entry->value) : nullptr;
    std::vector<Reply> out;
    for (size_t i = 2; i < args.size(); ++i) {
      if (!hash) {
        out.push_back(Reply::null());
        continue;
      }
      auto it = hash->find(args[i]);
      out.push_back(it == hash->end() ? Reply::null() : Reply::bulk(it->second));
    }
    return res(Reply::array(std::move(out)));
  });
  reg(table, "HDEL", -3, kWrite | kFast, [](Client& client, Args& args) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    Entry* entry = client.srv.store.lookup_write(args[1], client.srv.store.now_ms());
    if (!entry) return res(Reply::integer_reply(0));
    if (value_type(entry->value) != ValueType::Hash) return res(wrongtype());
    Hash& hash = std::get<Hash>(entry->value);
    int64_t removed = 0;
    for (size_t i = 2; i < args.size(); ++i) removed += hash.erase(args[i]);
    if (hash.empty()) client.srv.store.data.erase(args[1]);
    else if (removed > 0) client.srv.store.touch(*entry);
    return res(Reply::integer_reply(removed), removed > 0);
  });
  reg(table, "HLEN", 2, kReadOnly | kFast, [](Client& client, Args& args) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    const Entry* entry = client.srv.store.lookup_read(args[1], client.srv.store.now_ms());
    if (!entry) return res(Reply::integer_reply(0));
    if (value_type(entry->value) != ValueType::Hash) return res(wrongtype());
    return res(Reply::integer_reply(static_cast<int64_t>(std::get<Hash>(entry->value).size())));
  });
  reg(table, "HEXISTS", 3, kReadOnly | kFast, [](Client& client, Args& args) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    const Entry* entry = client.srv.store.lookup_read(args[1], client.srv.store.now_ms());
    if (!entry) return res(Reply::integer_reply(0));
    if (value_type(entry->value) != ValueType::Hash) return res(wrongtype());
    const Hash& hash = std::get<Hash>(entry->value);
    return res(Reply::integer_reply(hash.find(args[2]) == hash.end() ? 0 : 1));
  });
  reg(table, "HKEYS", 2, kReadOnly, [](Client& client, Args& args) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    const Entry* entry = client.srv.store.lookup_read(args[1], client.srv.store.now_ms());
    if (!entry) return res(Reply::array({}));
    if (value_type(entry->value) != ValueType::Hash) return res(wrongtype());
    std::vector<Reply> out;
    for (const auto& field : sorted_keys(std::get<Hash>(entry->value))) out.push_back(Reply::bulk(field));
    return res(Reply::array(std::move(out)));
  });
  reg(table, "HVALS", 2, kReadOnly, [](Client& client, Args& args) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    const Entry* entry = client.srv.store.lookup_read(args[1], client.srv.store.now_ms());
    if (!entry) return res(Reply::array({}));
    if (value_type(entry->value) != ValueType::Hash) return res(wrongtype());
    const Hash& hash = std::get<Hash>(entry->value);
    std::vector<Reply> out;
    for (const auto& field : sorted_keys(hash)) out.push_back(Reply::bulk(hash.at(field)));
    return res(Reply::array(std::move(out)));
  });
  reg(table, "HGETALL", 2, kReadOnly, [](Client& client, Args& args) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    const Entry* entry = client.srv.store.lookup_read(args[1], client.srv.store.now_ms());
    if (!entry) return res(Reply::array({}));
    if (value_type(entry->value) != ValueType::Hash) return res(wrongtype());
    const Hash& hash = std::get<Hash>(entry->value);
    std::vector<std::pair<Reply, Reply>> pairs;
    for (const auto& field : sorted_keys(hash)) {
      pairs.push_back({Reply::bulk(field), Reply::bulk(hash.at(field))});
    }
    return res(Reply::map(std::move(pairs)));
  });
  reg(table, "HINCRBY", 4, kWrite | kFast, [](Client& client, Args& args) -> CommandResult {
    int64_t delta = parse_int(args[3]);
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    Entry* entry = client.srv.store.lookup_write(args[1], client.srv.store.now_ms());
    if (!entry) {
      entry = &client.srv.store.create(args[1]);
      entry->value = Hash{};
    }
    if (value_type(entry->value) != ValueType::Hash) return res(wrongtype());
    Hash& hash = std::get<Hash>(entry->value);
    int64_t current = 0;
    auto it = hash.find(args[2]);
    if (it != hash.end()) {
      try {
        current = parse_int(it->second);
      } catch (const RedisError&) {
        return res(Reply::error("ERR hash value is not an integer"));
      }
    }
    int64_t next;
    if (__builtin_add_overflow(current, delta, &next)) {
      return res(Reply::error("ERR increment or decrement would overflow"));
    }
    hash[args[2]] = std::to_string(next);
    client.srv.store.touch(*entry);
    return res(Reply::integer_reply(next), true);
  });
  reg(table, "HSTRLEN", 3, kReadOnly | kFast, [](Client& client, Args& args) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    const Entry* entry = client.srv.store.lookup_read(args[1], client.srv.store.now_ms());
    if (!entry) return res(Reply::integer_reply(0));
    if (value_type(entry->value) != ValueType::Hash) return res(wrongtype());
    const Hash& hash = std::get<Hash>(entry->value);
    auto it = hash.find(args[2]);
    return res(Reply::integer_reply(it == hash.end() ? 0 : static_cast<int64_t>(it->second.size())));
  });
  reg(table, "HSCAN", -3, kReadOnly, [](Client& client, Args& args) -> CommandResult {
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
    if (value_type(entry->value) != ValueType::Hash) return res(wrongtype());
    const Hash& hash = std::get<Hash>(entry->value);
    auto fields = sorted_keys(hash);
    std::vector<Reply> items;
    size_t examined = 0;
    while (cursor < fields.size() && examined < static_cast<size_t>(count)) {
      if (glob_match_local(pattern, fields[cursor])) {
        items.push_back(Reply::bulk(fields[cursor]));
        items.push_back(Reply::bulk(hash.at(fields[cursor])));
      }
      ++cursor;
      ++examined;
    }
    if (cursor >= fields.size()) cursor = 0;
    return res(Reply::array({Reply::bulk(std::to_string(cursor)), Reply::array(std::move(items))}));
  });
  reg(table, "HRANDFIELD", -2, kReadOnly, [](Client& client, Args& args) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    const Entry* entry = client.srv.store.lookup_read(args[1], client.srv.store.now_ms());
    if (!entry) return res(args.size() == 2 ? Reply::null() : Reply::array({}));
    if (value_type(entry->value) != ValueType::Hash) return res(wrongtype());
    const Hash& hash = std::get<Hash>(entry->value);
    auto fields = sorted_keys(hash);
    if (args.size() == 2) {
      if (fields.empty()) return res(Reply::null());
      return res(Reply::bulk(fields.front()));
    }
    int64_t count = parse_int(args[2]);
    bool with_values = args.size() == 4 && upper_ascii(args[3]) == "WITHVALUES";
    std::vector<Reply> out;
    if (count < 0) {
      count = -count;
      for (int64_t i = 0; i < count && !fields.empty(); ++i) {
        const std::string& field = fields[static_cast<size_t>(i) % fields.size()];
        if (with_values) {
          out.push_back(Reply::bulk(field));
          out.push_back(Reply::bulk(hash.at(field)));
        } else {
          out.push_back(Reply::bulk(field));
        }
      }
    } else {
      for (int64_t i = 0; i < count && i < static_cast<int64_t>(fields.size()); ++i) {
        const std::string& field = fields[static_cast<size_t>(i)];
        if (with_values) {
          out.push_back(Reply::bulk(field));
          out.push_back(Reply::bulk(hash.at(field)));
        } else {
          out.push_back(Reply::bulk(field));
        }
      }
    }
    return res(Reply::array(std::move(out)));
  });
}

}  // namespace redlite
