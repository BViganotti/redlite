#include "server.hpp"

#include <algorithm>
#include <cmath>
#include <cstring>

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

constexpr int64_t kMaxString = 512LL * 1024 * 1024;
}  // namespace

void register_string_commands(CommandMap& table) {
  reg(table, "GET", 2, kReadOnly | kFast, [](Client& client, Args& args) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    const Entry* entry = client.srv.store.lookup_read(args[1], client.srv.store.now_ms());
    if (!entry) {
      ++client.srv.store.misses;
      return res(Reply::null());
    }
    if (value_type(entry->value) != ValueType::String) {
      return res(Reply::error("WRONGTYPE Operation against a key holding the wrong kind of value"));
    }
    ++client.srv.store.hits;
    return res(Reply::bulk(std::get<String>(entry->value)));
  });

  reg(table, "SET", -3, kWrite, [](Client& client, Args& args) -> CommandResult {
    bool nx = false, xx = false, get = false, keepttl = false, has_expire = false;
    int64_t expire_at = 0;
    int64_t now = client.srv.store.now_ms();
    for (size_t i = 3; i < args.size(); ++i) {
      std::string option = upper_ascii(args[i]);
      if (option == "NX") {
        if (nx || xx) return res(Reply::error("ERR syntax error"));
        nx = true;
      } else if (option == "XX") {
        if (nx || xx) return res(Reply::error("ERR syntax error"));
        xx = true;
      } else if (option == "GET") {
        get = true;
      } else if (option == "KEEPTTL") {
        if (keepttl || has_expire) return res(Reply::error("ERR syntax error"));
        keepttl = true;
      } else if (option == "EX" || option == "PX" || option == "EXAT" || option == "PXAT") {
        if (has_expire || keepttl || i + 1 >= args.size()) return res(Reply::error("ERR syntax error"));
        int64_t amount = parse_int(args[++i]);
        if (option == "EX") expire_at = now + amount * 1000;
        else if (option == "PX") expire_at = now + amount;
        else if (option == "EXAT") expire_at = amount * 1000;
        else expire_at = amount;
        has_expire = true;
      } else {
        return res(Reply::error("ERR syntax error"));
      }
    }

    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    int64_t now2 = client.srv.store.now_ms();
    Entry* entry = client.srv.store.lookup_write(args[1], now2);
    bool exists = entry != nullptr;
    bool had_old = false;
    std::string old;
    if (exists) {
      if (value_type(entry->value) == ValueType::String) {
        old = std::get<String>(entry->value);
        had_old = true;
      } else if (get) {
        return res(wrongtype());
      }
    }
    if ((nx && exists) || (xx && !exists)) {
      if (get) return res(had_old ? Reply::bulk(old) : Reply::null());
      return res(Reply::null());
    }

    int64_t final_expire = 0;
    if (keepttl && exists) final_expire = entry->expire_at;
    else if (has_expire) final_expire = expire_at;
    if (final_expire != 0 && final_expire <= now2) {
      client.srv.store.data.erase(args[1]);
      return res(get ? (had_old ? Reply::bulk(old) : Reply::null()) : Reply::null(), true,
                 {"DEL", args[1]});
    }
    if (!exists) entry = &client.srv.store.create(args[1]);
    entry->value = String(args[2]);
    entry->expire_at = final_expire;
    client.srv.store.touch(*entry);

    std::vector<std::string> rewrite = {"SET", args[1], args[2]};
    if (keepttl) rewrite.push_back("KEEPTTL");
    else if (has_expire) {
      rewrite.push_back("PXAT");
      rewrite.push_back(std::to_string(final_expire));
    }
    if (get) return res(had_old ? Reply::bulk(old) : Reply::null(), true, rewrite);
    return res(Reply::simple("OK"), true, rewrite);
  });

  reg(table, "SETNX", 3, kWrite | kFast, [](Client& client, Args& args) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    int64_t now = client.srv.store.now_ms();
    if (client.srv.store.lookup_write(args[1], now)) return res(Reply::integer_reply(0));
    Entry& entry = client.srv.store.create(args[1]);
    entry.value = String(args[2]);
    client.srv.store.touch(entry);
    return res(Reply::integer_reply(1), true);
  });

  auto setex = [](Client& client, Args& args, int64_t unit) -> CommandResult {
    int64_t amount = parse_int(args[2]);
    if (amount <= 0) return res(Reply::error("ERR invalid expire time in 'setex' command"));
    int64_t at = client.srv.store.now_ms() + amount * unit;
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    Entry* entry = client.srv.store.lookup_write(args[1], client.srv.store.now_ms());
    if (!entry) entry = &client.srv.store.create(args[1]);
    entry->value = String(args[3]);
    entry->expire_at = at;
    client.srv.store.touch(*entry);
    return res(Reply::simple("OK"), true,
               {"SET", args[1], args[3], "PXAT", std::to_string(at)});
  };
  reg(table, "SETEX", 4, kWrite, [setex](Client& c, Args& a) { return setex(c, a, 1000); });
  reg(table, "PSETEX", 4, kWrite, [setex](Client& c, Args& a) { return setex(c, a, 1); });

  reg(table, "GETSET", 3, kWrite | kFast, [](Client& client, Args& args) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    Entry* entry = client.srv.store.lookup_write(args[1], client.srv.store.now_ms());
    bool had_old = false;
    std::string old;
    if (entry) {
      if (value_type(entry->value) != ValueType::String) return res(wrongtype());
      old = std::get<String>(entry->value);
      had_old = true;
    }
    if (!entry) entry = &client.srv.store.create(args[1]);
    entry->value = String(args[2]);
    client.srv.store.touch(*entry);
    return res(had_old ? Reply::bulk(old) : Reply::null(), true);
  });

  reg(table, "GETDEL", 2, kWrite | kFast, [](Client& client, Args& args) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    Entry* entry = client.srv.store.lookup_write(args[1], client.srv.store.now_ms());
    if (!entry) return res(Reply::null());
    if (value_type(entry->value) != ValueType::String) return res(wrongtype());
    std::string value = std::get<String>(entry->value);
    client.srv.store.data.erase(args[1]);
    return res(Reply::bulk(value), true, {"DEL", args[1]});
  });

  reg(table, "GETEX", -2, kWrite | kFast, [](Client& client, Args& args) -> CommandResult {
    bool has_expire = false, persist = false;
    int64_t expire_at = 0;
    int64_t now = client.srv.store.now_ms();
    for (size_t i = 2; i < args.size(); ++i) {
      std::string option = upper_ascii(args[i]);
      if (option == "PERSIST") {
        if (has_expire || persist) return res(Reply::error("ERR syntax error"));
        persist = true;
      } else if (option == "EX" || option == "PX" || option == "EXAT" || option == "PXAT") {
        if (has_expire || persist || i + 1 >= args.size()) return res(Reply::error("ERR syntax error"));
        int64_t amount = parse_int(args[++i]);
        if (option == "EX") expire_at = now + amount * 1000;
        else if (option == "PX") expire_at = now + amount;
        else if (option == "EXAT") expire_at = amount * 1000;
        else expire_at = amount;
        has_expire = true;
      } else {
        return res(Reply::error("ERR syntax error"));
      }
    }
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    Entry* entry = client.srv.store.lookup_write(args[1], client.srv.store.now_ms());
    if (!entry) return res(Reply::null());
    if (value_type(entry->value) != ValueType::String) return res(wrongtype());
    std::string value = std::get<String>(entry->value);
    if (has_expire) {
      entry->expire_at = expire_at;
      client.srv.store.touch(*entry);
      return res(Reply::bulk(value), true,
                 {"PEXPIREAT", args[1], std::to_string(expire_at)});
    }
    if (persist) {
      if (entry->expire_at != 0) {
        entry->expire_at = 0;
        client.srv.store.touch(*entry);
        return res(Reply::bulk(value), true, {"PERSIST", args[1]});
      }
    }
    return res(Reply::bulk(value));
  });

  reg(table, "MGET", -2, kReadOnly | kFast, [](Client& client, Args& args) -> CommandResult {
    std::vector<Reply> out;
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    int64_t now = client.srv.store.now_ms();
    for (size_t i = 1; i < args.size(); ++i) {
      const Entry* entry = client.srv.store.lookup_read(args[i], now);
      if (!entry || value_type(entry->value) != ValueType::String) {
        out.push_back(Reply::null());
      } else {
        out.push_back(Reply::bulk(std::get<String>(entry->value)));
      }
    }
    return res(Reply::array(std::move(out)));
  });

  reg(table, "MSET", -3, kWrite, [](Client& client, Args& args) -> CommandResult {
    if ((args.size() - 1) % 2 != 0) return res(Reply::error("ERR wrong number of arguments for 'mset' command"));
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    for (size_t i = 1; i + 1 < args.size(); i += 2) {
      Entry* entry = client.srv.store.lookup_write(args[i], client.srv.store.now_ms());
      if (!entry) entry = &client.srv.store.create(args[i]);
      entry->value = String(args[i + 1]);
      entry->expire_at = 0;
      client.srv.store.touch(*entry);
    }
    return res(Reply::simple("OK"), true);
  });

  reg(table, "MSETNX", -3, kWrite, [](Client& client, Args& args) -> CommandResult {
    if ((args.size() - 1) % 2 != 0) return res(Reply::error("ERR wrong number of arguments for 'msetnx' command"));
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    int64_t now = client.srv.store.now_ms();
    for (size_t i = 1; i + 1 < args.size(); i += 2) {
      if (client.srv.store.lookup_read(args[i], now)) return res(Reply::integer_reply(0));
    }
    for (size_t i = 1; i + 1 < args.size(); i += 2) {
      Entry& entry = client.srv.store.create(args[i]);
      entry.value = String(args[i + 1]);
      entry.expire_at = 0;
      client.srv.store.touch(entry);
    }
    return res(Reply::integer_reply(1), true);
  });

  reg(table, "APPEND", 3, kWrite, [](Client& client, Args& args) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    Entry* entry = client.srv.store.lookup_write(args[1], client.srv.store.now_ms());
    if (!entry) {
      entry = &client.srv.store.create(args[1]);
      entry->value = String(args[2]);
      client.srv.store.touch(*entry);
      return res(Reply::integer_reply(static_cast<int64_t>(args[2].size())), true);
    }
    if (value_type(entry->value) != ValueType::String) return res(wrongtype());
    String& value = std::get<String>(entry->value);
    if (value.size() + args[2].size() > static_cast<size_t>(kMaxString)) {
      return res(Reply::error("ERR string exceeds maximum allowed size (proto-max-bulk-len)"));
    }
    value += args[2];
    client.srv.store.touch(*entry);
    return res(Reply::integer_reply(static_cast<int64_t>(value.size())), true);
  });

  reg(table, "STRLEN", 2, kReadOnly | kFast, [](Client& client, Args& args) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    const Entry* entry = client.srv.store.lookup_read(args[1], client.srv.store.now_ms());
    if (!entry) return res(Reply::integer_reply(0));
    if (value_type(entry->value) != ValueType::String) return res(wrongtype());
    return res(Reply::integer_reply(static_cast<int64_t>(std::get<String>(entry->value).size())));
  });

  reg(table, "GETRANGE", 4, kReadOnly, [](Client& client, Args& args) -> CommandResult {
    int64_t start = parse_int(args[2]);
    int64_t end = parse_int(args[3]);
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    const Entry* entry = client.srv.store.lookup_read(args[1], client.srv.store.now_ms());
    if (!entry) return res(Reply::bulk(""));
    if (value_type(entry->value) != ValueType::String) return res(wrongtype());
    const String& value = std::get<String>(entry->value);
    if (value.empty()) return res(Reply::bulk(""));
    int64_t length = static_cast<int64_t>(value.size());
    if (start < 0) start += length;
    if (end < 0) end += length;
    if (start < 0) start = 0;
    if (end >= length) end = length - 1;
    if (start > end || start >= length) return res(Reply::bulk(""));
    return res(Reply::bulk(value.substr(static_cast<size_t>(start),
                                        static_cast<size_t>(end - start + 1))));
  });
  reg(table, "SUBSTR", 4, kReadOnly, table.at("GETRANGE").handler);

  reg(table, "SETRANGE", 4, kWrite, [](Client& client, Args& args) -> CommandResult {
    int64_t offset = parse_int(args[2]);
    if (offset < 0) return res(Reply::error("ERR offset is out of range"));
    if (offset > kMaxString) return res(Reply::error("ERR offset is out of range"));
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    Entry* entry = client.srv.store.lookup_write(args[1], client.srv.store.now_ms());
    if (!entry) {
      entry = &client.srv.store.create(args[1]);
      entry->value = String{};
    }
    if (value_type(entry->value) != ValueType::String) return res(wrongtype());
    String& value = std::get<String>(entry->value);
    size_t end = static_cast<size_t>(offset) + args[3].size();
    if (end > static_cast<size_t>(kMaxString)) {
      return res(Reply::error("ERR string exceeds maximum allowed size (proto-max-bulk-len)"));
    }
    if (value.size() < end) value.resize(end, '\0');
    std::copy(args[3].begin(), args[3].end(), value.begin() + offset);
    client.srv.store.touch(*entry);
    return res(Reply::integer_reply(static_cast<int64_t>(value.size())), true);
  });

  auto incr = [](Client& client, Args& args, int64_t delta) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    Entry* entry = client.srv.store.lookup_write(args[1], client.srv.store.now_ms());
    int64_t current = 0;
    if (entry) {
      if (value_type(entry->value) != ValueType::String) return res(wrongtype());
      try {
        current = parse_int(std::get<String>(entry->value));
      } catch (const RedisError&) {
        return res(Reply::error("ERR value is not an integer or out of range"));
      }
    }
    int64_t next;
    if (__builtin_add_overflow(current, delta, &next)) {
      return res(Reply::error("ERR increment or decrement would overflow"));
    }
    if (!entry) entry = &client.srv.store.create(args[1]);
    entry->value = String(std::to_string(next));
    client.srv.store.touch(*entry);
    return res(Reply::integer_reply(next), true);
  };
  reg(table, "INCR", 2, kWrite | kFast, [incr](Client& c, Args& a) { return incr(c, a, 1); });
  reg(table, "DECR", 2, kWrite | kFast, [incr](Client& c, Args& a) { return incr(c, a, -1); });
  reg(table, "INCRBY", 3, kWrite | kFast, [incr](Client& c, Args& a) {
    return incr(c, a, parse_int(a[2]));
  });
  reg(table, "DECRBY", 3, kWrite | kFast, [incr](Client& c, Args& a) {
    int64_t delta = parse_int(a[2]);
    if (delta == INT64_MIN) return res(Reply::error("ERR decrement would overflow"));
    return incr(c, a, -delta);
  });

  reg(table, "INCRBYFLOAT", 3, kWrite | kFast, [](Client& client, Args& args) -> CommandResult {
    double delta = parse_float(args[2]);
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    Entry* entry = client.srv.store.lookup_write(args[1], client.srv.store.now_ms());
    double current = 0;
    if (entry) {
      if (value_type(entry->value) != ValueType::String) return res(wrongtype());
      try {
        current = parse_float(std::get<String>(entry->value));
      } catch (const RedisError&) {
        return res(Reply::error("ERR value is not a valid float"));
      }
    }
    double result = current + delta;
    if (!std::isfinite(result)) {
      return res(Reply::error("ERR increment would produce NaN or Infinity"));
    }
    std::string text = format_double(result);
    if (!entry) entry = &client.srv.store.create(args[1]);
    entry->value = String(text);
    client.srv.store.touch(*entry);
    return res(Reply::bulk(text), true);
  });
}

}  // namespace redlite
