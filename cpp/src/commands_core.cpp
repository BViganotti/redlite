#include "server.hpp"

#include <algorithm>
#include <chrono>
#include <cmath>
#include <cstring>
#include <sstream>

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

Reply int_reply(int64_t value) { return Reply::integer_reply(value); }
Reply ok_reply() { return Reply::simple("OK"); }
Reply err_syntax() { return Reply::error("ERR syntax error"); }




std::vector<std::string> live_keys(Store& store, int64_t now) {
  std::vector<std::string> keys;
  keys.reserve(store.data.size());
  for (const auto& [key, entry] : store.data) {
    if (!Store::expired(entry, now)) keys.push_back(key);
  }
  std::sort(keys.begin(), keys.end());
  return keys;
}

}  // namespace

bool glob_match_local(const std::string& pattern, const std::string& value) {
  size_t p = 0, v = 0;
  while (p < pattern.size()) {
    char ch = pattern[p];
    if (ch == '*') {
      while (p < pattern.size() && pattern[p] == '*') ++p;
      if (p == pattern.size()) return true;
      for (size_t skip = v; skip <= value.size(); ++skip) {
        if (glob_match_local(pattern.substr(p), value.substr(skip))) return true;
      }
      return false;
    }
    if (v >= value.size()) return false;
    if (ch == '?') {
      ++p;
      ++v;
      continue;
    }
    if (ch == '\\' && p + 1 < pattern.size()) {
      ++p;
      ch = pattern[p];
      if (ch != value[v]) return false;
      ++p;
      ++v;
      continue;
    }
    if (ch == '[') {
      ++p;
      bool negate = false;
      if (p < pattern.size() && (pattern[p] == '^' || pattern[p] == '!')) {
        negate = true;
        ++p;
      }
      bool matched = false;
      bool closed = false;
      while (p < pattern.size()) {
        if (pattern[p] == ']') {
          closed = true;
          ++p;
          break;
        }
        char first = pattern[p++];
        if (first == '\\' && p < pattern.size()) first = pattern[p++];
        char last = first;
        if (p + 1 < pattern.size() && pattern[p] == '-') {
          ++p;
          last = pattern[p++];
        }
        if (value[v] >= first && value[v] <= last) matched = true;
      }
      if (!closed) return false;
      if (matched == negate) return false;
      ++v;
      continue;
    }
    if (ch != value[v]) return false;
    ++p;
    ++v;
  }
  return v == value.size();
}

void register_core_commands(CommandMap& table) {
  // ------------------------------------------------------------ connection
  reg(table, "PING", -1, kFast, [](Client& client, Args& args) -> CommandResult {
    if (args.size() > 2) return res(Reply::error("ERR wrong number of arguments for 'ping' command"));
    if (client.subscribed()) {
      return res(Reply::push({Reply::bulk("pong"), Reply::bulk(args.size() == 2 ? args[1] : "")}));
    }
    return res(args.size() == 2 ? Reply::bulk(args[1]) : Reply::simple("PONG"));
  });
  reg(table, "ECHO", 2, kFast, [](Client&, Args& args) -> CommandResult {
    return res(Reply::bulk(args[1]));
  });
  reg(table, "QUIT", 1, kFast, [](Client& client, Args&) -> CommandResult {
    client.closing.store(true);
    return res(ok_reply());
  });
  reg(table, "SELECT", 2, kFast, [](Client&, Args& args) -> CommandResult {
    int64_t index = parse_int(args[1]);
    if (index != 0) return res(Reply::error("ERR DB index is out of range"));
    return res(ok_reply());
  });
  reg(table, "HELLO", -1, kFast, [](Client& client, Args& args) -> CommandResult {
    int proto = client.proto;
    size_t next = 1;
    if (next < args.size()) {
      int64_t value = parse_int(args[next]);
      if (value != 2 && value != 3) {
        return res(Reply::error("NOPROTO unsupported protocol version"));
      }
      proto = static_cast<int>(value);
      ++next;
    }
    while (next < args.size()) {
      std::string option = upper_ascii(args[next]);
      if (option == "AUTH") {
        if (next + 2 >= args.size()) return res(err_syntax());
        next += 3;
      } else if (option == "SETNAME") {
        if (next + 1 >= args.size()) return res(err_syntax());
        client.name = args[next + 1];
        next += 2;
      } else {
        return res(err_syntax());
      }
    }
    client.proto = proto;
    std::vector<std::pair<Reply, Reply>> pairs = {
        {Reply::bulk("server"), Reply::bulk("redis")},
        {Reply::bulk("version"), Reply::bulk("8.8.0")},
        {Reply::bulk("proto"), int_reply(proto)},
        {Reply::bulk("id"), int_reply(static_cast<int64_t>(client.id))},
        {Reply::bulk("mode"), Reply::bulk("standalone")},
        {Reply::bulk("role"), Reply::bulk("master")},
        {Reply::bulk("modules"), Reply::array({})},
    };
    return res(Reply::map(std::move(pairs)));
  });
  reg(table, "RESET", 1, kFast, [](Client& client, Args&) -> CommandResult {
    client.multi = false;
    client.dirty = false;
    client.queued.clear();
    client.watched.clear();
    client.reply_off = false;
    client.reply_skip = false;
    client.proto = 2;
    client.srv.remove_subscriptions(client);
    client.subs.clear();
    client.psubs.clear();
    client.ssubs.clear();
    return res(Reply::simple("RESET"));
  });

  // ------------------------------------------------------------ client/command
  reg(table, "CLIENT", -2, kAdmin, [](Client& client, Args& args) -> CommandResult {
    std::string sub = upper_ascii(args[1]);
    if (sub == "ID") return res(int_reply(static_cast<int64_t>(client.id)));
    if (sub == "GETNAME") return res(Reply::bulk(client.name));
    if (sub == "SETNAME") {
      if (args.size() != 3) return res(err_syntax());
      client.name = args[2];
      return res(ok_reply());
    }
    if (sub == "SETINFO") return res(ok_reply());
    if (sub == "LIST") return res(Reply::bulk(client.srv.client_list()));
    if (sub == "INFO") return res(Reply::bulk(client.info_line()));
    if (sub == "GETREDIR") return res(int_reply(-1));
    if (sub == "REPLY") {
      if (args.size() != 3) {
        return res(Reply::error("ERR wrong number of arguments for 'client|reply' command"));
      }
      std::string mode = upper_ascii(args[2]);
      if (mode == "ON") {
        client.reply_off = false;
        client.reply_skip = false;
        return res(ok_reply());
      }
      if (mode == "OFF") {
        client.reply_off = true;
        return res(Reply::multi({}));
      }
      if (mode == "SKIP") {
        client.reply_skip = true;
        return res(Reply::multi({}));
      }
      return res(err_syntax());
    }
    if (sub == "NO-EVICT" || sub == "NO-TOUCH" || sub == "UNPAUSE" || sub == "PAUSE") {
      return res(ok_reply());
    }
    if (sub == "UNBLOCK") return res(int_reply(0));
    return res(Reply::error("ERR unknown subcommand '" + args[1] + "' for CLIENT"));
  });

  reg(table, "COMMAND", -1, kAdmin, [](Client& client, Args& args) -> CommandResult {
    (void)client;
    auto& table = command_table();
    if (args.size() == 1) {
      std::vector<Reply> out;
      std::vector<std::string> names;
      for (const auto& [name, _] : table) names.push_back(name);
      std::sort(names.begin(), names.end());
      for (const auto& name : names) {
        const Command& cmd = table.at(name);
        out.push_back(Reply::array({
            Reply::bulk(lower_ascii(cmd.name)),
            int_reply(cmd.arity),
            Reply::array({}),
            int_reply(0), int_reply(0), int_reply(0),
            Reply::array({}), Reply::array({}), Reply::array({}), Reply::array({}),
        }));
      }
      return res(Reply::array(std::move(out)));
    }
    std::string sub = upper_ascii(args[1]);
    if (sub == "COUNT") return res(int_reply(static_cast<int64_t>(table.size())));
    if (sub == "INFO") {
      if (args.size() == 2) return res(Reply::array({}));
      std::vector<Reply> out;
      for (size_t i = 2; i < args.size(); ++i) {
        auto it = table.find(upper_ascii(args[i]));
        if (it == table.end()) {
          out.push_back(Reply::null());
          continue;
        }
        out.push_back(Reply::array({
            Reply::bulk(lower_ascii(it->second.name)),
            int_reply(it->second.arity),
            Reply::array({}),
            int_reply(0), int_reply(0), int_reply(0),
            Reply::array({}), Reply::array({}), Reply::array({}), Reply::array({}),
        }));
      }
      return res(Reply::array(std::move(out)));
    }
    if (sub == "DOCS") return res(Reply::map({}));
    if (sub == "LIST") {
      std::vector<std::string> names;
      for (const auto& [name, _] : table) names.push_back(lower_ascii(name));
      std::sort(names.begin(), names.end());
      if (args.size() >= 3) {
        std::string filter = upper_ascii(args[2]);
        if (filter == "FILTERBY" && args.size() == 5) {
          std::string kind = upper_ascii(args[3]);
          std::string value = args[4];
          std::vector<std::string> filtered;
          if (kind == "PATTERN") {
            for (const auto& name : names) {
              if (glob_match_local(value, name)) filtered.push_back(name);
            }
          } else if (kind == "MODULE") {
            // no modules
          } else if (kind == "ACLCAT") {
            // Approximate: read/write flags only.
            for (const auto& [name, cmd] : table) {
              if ((value == "read" && (cmd.flags & kReadOnly)) ||
                  (value == "write" && (cmd.flags & kWrite)) ||
                  (value == "fast" && (cmd.flags & kFast)) ||
                  (value == "admin" && (cmd.flags & kAdmin)) ||
                  value == "all") {
                filtered.push_back(lower_ascii(name));
              }
            }
            std::sort(filtered.begin(), filtered.end());
          }
          names = filtered;
        } else {
          return res(err_syntax());
        }
      }
      std::vector<Reply> out;
      for (const auto& name : names) out.push_back(Reply::bulk(name));
      return res(Reply::array(std::move(out)));
    }
    if (sub == "GETKEYS") {
      if (args.size() < 3) return res(err_syntax());
      return res(Reply::array({}));
    }
    return res(Reply::error("ERR unknown subcommand '" + args[1] + "' for COMMAND"));
  });

  reg(table, "CONFIG", -2, kAdmin, [](Client& client, Args& args) -> CommandResult {
    std::string sub = upper_ascii(args[1]);
    if (sub == "GET") {
      if (args.size() < 3) return res(err_syntax());
      auto snapshot = client.srv.config_snapshot();
      std::vector<std::pair<Reply, Reply>> pairs;
      for (const auto& [name, value] : snapshot) {
        bool match = false;
        for (size_t i = 2; i < args.size(); ++i) {
          if (glob_match_local(args[i], name)) {
            match = true;
            break;
          }
        }
        if (match) pairs.push_back({Reply::bulk(name), Reply::bulk(value)});
      }
      return res(Reply::map(std::move(pairs)));
    }
    if (sub == "SET") {
      if (args.size() != 4) return res(err_syntax());
      client.srv.set_config(args[2], args[3]);
      return res(ok_reply());
    }
    if (sub == "RESETSTAT") {
      client.srv.store.hits = 0;
      client.srv.store.misses = 0;
      client.srv.store.expired_keys = 0;
      client.srv.total_commands.store(0);
      return res(ok_reply());
    }
    if (sub == "REWRITE") {
      return res(Reply::error("ERR The server is running without a config file"));
    }
    return res(Reply::error("ERR unknown subcommand '" + args[1] + "' for CONFIG"));
  });

  reg(table, "INFO", -1, kAdmin, [](Client& client, Args& args) -> CommandResult {
    std::vector<std::string> sections(args.begin() + 1, args.end());
    return res(Reply::bulk(client.srv.info(sections)));
  });
  reg(table, "TIME", 1, kFast, [](Client&, Args&) -> CommandResult {
    auto now = std::chrono::system_clock::now();
    auto seconds = std::chrono::duration_cast<std::chrono::seconds>(now.time_since_epoch()).count();
    auto micros = std::chrono::duration_cast<std::chrono::microseconds>(now.time_since_epoch()).count() % 1000000;
    return res(Reply::array({Reply::bulk(std::to_string(seconds)), Reply::bulk(std::to_string(micros))}));
  });
  reg(table, "DBSIZE", 1, kFast, [](Client& client, Args&) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    return res(int_reply(static_cast<int64_t>(client.srv.store.size())));
  });
  reg(table, "FLUSHDB", -1, kWrite, [](Client& client, Args&) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    client.srv.store.flush();
    return res(ok_reply(), true, {"FLUSHALL"});
  });
  reg(table, "FLUSHALL", -1, kWrite, [](Client& client, Args&) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    client.srv.store.flush();
    return res(ok_reply(), true, {"FLUSHALL"});
  });
  reg(table, "LASTSAVE", 1, kFast, [](Client& client, Args&) -> CommandResult {
    return res(int_reply(static_cast<int64_t>(client.srv.started_ms / 1000)));
  });
  reg(table, "BGREWRITEAOF", 1, kAdmin, [](Client& client, Args&) -> CommandResult {
    try {
      client.srv.bgrewrite_aof();
    } catch (const RedisError& error) {
      return res(Reply::error(error.what()));
    }
    return res(Reply::simple("Background append only file rewriting started"));
  });
  reg(table, "SAVE", -1, kAdmin, [](Client& client, Args&) -> CommandResult {
    if (client.srv.config().appendonly) {
      client.srv.bgrewrite_aof();
      return res(ok_reply());
    }
    return res(Reply::error("ERR SAVE is not implemented without appendonly in the C++ port"));
  });
  reg(table, "BGSAVE", -1, kAdmin, [](Client& client, Args&) -> CommandResult {
    if (client.srv.config().appendonly) {
      client.srv.bgrewrite_aof();
      return res(Reply::simple("Background saving started"));
    }
    return res(Reply::error("ERR BGSAVE is not implemented without appendonly in the C++ port"));
  });

  reg(table, "DEBUG", -2, kAdmin, [](Client& client, Args& args) -> CommandResult {
    std::string sub = upper_ascii(args[1]);
    if (sub == "SLEEP" && args.size() == 3) {
      double seconds = parse_float(args[2]);
      if (seconds < 0) return res(err_syntax());
      std::this_thread::sleep_for(std::chrono::duration<double>(seconds));
      return res(ok_reply());
    }
    if (sub == "OBJECT" && args.size() == 3) {
      std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
      const Entry* entry = client.srv.store.lookup_read(args[2], client.srv.store.now_ms());
      if (!entry) return res(Reply::error("ERR no such key"));
      const char* type = type_name(value_type(entry->value));
      std::string encoding = "raw";
      if (value_type(entry->value) == ValueType::Hash) encoding = "hashtable";
      if (value_type(entry->value) == ValueType::List) encoding = "quicklist";
      if (value_type(entry->value) == ValueType::Set) encoding = "hashtable";
      if (value_type(entry->value) == ValueType::ZSet) encoding = "skiplist";
      (void)type;
      return res(Reply::bulk(std::string("Value at:0x0 refcount:1 encoding:") + encoding +
                             " serializedlength:0 lru:0 lru_seconds_idle:0"));
    }
    if (sub == "JMAP" || sub == "SET-ACTIVE-EXPIRE" || sub == "CHANGE-REPL-ID" ||
        sub == "QUICKLIST-PACKED-THRESHOLD" || sub == "STRINGMATCH-LEN") {
      return res(ok_reply());
    }
    if (sub == "FLUSHALL") {
      std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
      client.srv.store.flush();
      return res(ok_reply(), true, {"FLUSHALL"});
    }
    return res(Reply::error("ERR DEBUG subcommand is not supported"));
  });
  reg(table, "MODULE", -2, kAdmin, [](Client&, Args& args) -> CommandResult {
    std::string sub = upper_ascii(args[1]);
    if (sub == "LIST") return res(Reply::array({}));
    if (sub == "HELP") return res(Reply::array({Reply::bulk("MODULE LIST")}));
    return res(Reply::error("ERR MODULE not supported by the C++ port"));
  });
  reg(table, "SLOWLOG", -2, kAdmin, [](Client&, Args& args) -> CommandResult {
    std::string sub = upper_ascii(args[1]);
    if (sub == "GET") return res(Reply::array({}));
    if (sub == "LEN") return res(int_reply(0));
    if (sub == "RESET") return res(ok_reply());
    return res(Reply::error("ERR unknown subcommand '" + args[1] + "' for SLOWLOG"));
  });
  reg(table, "LATENCY", -2, kAdmin, [](Client&, Args& args) -> CommandResult {
    std::string sub = upper_ascii(args[1]);
    if (sub == "LATEST") return res(Reply::array({}));
    if (sub == "RESET") return res(int_reply(0));
    if (sub == "DOCTOR") return res(Reply::bulk("Dave, no latency spike was observed."));
    if (sub == "HELP") return res(Reply::array({Reply::bulk("LATENCY LATEST|RESET|DOCTOR")}));
    return res(Reply::array({}));
  });
  reg(table, "MONITOR", 1, kAdmin, [](Client&, Args&) -> CommandResult {
    return res(ok_reply());
  });
  reg(table, "ACL", -2, kAdmin, [](Client&, Args& args) -> CommandResult {
    std::string sub = upper_ascii(args[1]);
    if (sub == "WHOAMI") return res(Reply::bulk("default"));
    if (sub == "USERS") return res(Reply::array({Reply::bulk("default")}));
    if (sub == "LIST") return res(Reply::array({Reply::bulk("user default on nopass ~* &* +@all")}));
    if (sub == "CAT") {
      if (args.size() == 2) {
        return res(Reply::array({Reply::bulk("keyspace"), Reply::bulk("read"), Reply::bulk("write"),
                                 Reply::bulk("admin"), Reply::bulk("pubsub"), Reply::bulk("fast"),
                                 Reply::bulk("slow")}));
      }
      std::vector<Reply> names;
      for (const auto& [name, cmd] : command_table()) {
        std::string category = lower_ascii(args[2]);
        if ((category == "read" && (cmd.flags & kReadOnly)) ||
            (category == "write" && (cmd.flags & kWrite)) ||
            (category == "fast" && (cmd.flags & kFast)) ||
            (category == "admin" && (cmd.flags & kAdmin))) {
          names.push_back(Reply::bulk(lower_ascii(name)));
        }
      }
      return res(Reply::array(std::move(names)));
    }
    if (sub == "GENPASS") {
      int bits = 256;
      if (args.size() == 3) bits = static_cast<int>(parse_int(args[2]));
      if (bits < 1 || bits > 4096) return res(Reply::error("ERR ACL GENPASS argument out of range"));
      int chars = (bits + 3) / 4;
      static const char* hex = "0123456789abcdef";
      std::string password;
      unsigned seed = static_cast<unsigned>(
          std::chrono::steady_clock::now().time_since_epoch().count());
      for (int i = 0; i < chars; ++i) {
        seed = seed * 1103515245 + 12345;
        password.push_back(hex[(seed >> 16) & 0xf]);
      }
      return res(Reply::bulk(password));
    }
    if (sub == "DRYRUN") return res(ok_reply());
    if (sub == "LOG") return res(Reply::array({}));
    return res(Reply::error("ERR unknown subcommand '" + args[1] + "' for ACL"));
  });
  reg(table, "WAIT", 3, kFast, [](Client&, Args&) -> CommandResult { return res(int_reply(0)); });
  reg(table, "WAITAOF", 4, kBlocking, [](Client&, Args&) -> CommandResult {
    return res(Reply::array({int_reply(0), int_reply(0)}));
  });

  // ------------------------------------------------------------ keyspace
  reg(table, "DEL", -2, kWrite, [](Client& client, Args& args) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    int64_t removed = 0;
    for (size_t i = 1; i < args.size(); ++i) {
      if (client.srv.store.del(args[i])) ++removed;
    }
    return res(int_reply(removed), removed > 0);
  });
  reg(table, "UNLINK", -2, kWrite | kFast, table.at("DEL").handler);
  reg(table, "EXISTS", -2, kReadOnly | kFast, [](Client& client, Args& args) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    int64_t now = client.srv.store.now_ms();
    int64_t count = 0;
    for (size_t i = 1; i < args.size(); ++i) {
      if (client.srv.store.lookup_read(args[i], now)) ++count;
    }
    return res(int_reply(count));
  });
  reg(table, "TOUCH", -2, kReadOnly | kFast, table.at("EXISTS").handler);
  reg(table, "TYPE", 2, kReadOnly | kFast, [](Client& client, Args& args) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    const Entry* entry = client.srv.store.lookup_read(args[1], client.srv.store.now_ms());
    if (!entry) return res(Reply::simple("none"));
    return res(Reply::simple(type_name(value_type(entry->value))));
  });
  reg(table, "KEYS", 2, kReadOnly, [](Client& client, Args& args) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    std::vector<Reply> out;
    for (const auto& key : live_keys(client.srv.store, client.srv.store.now_ms())) {
      if (glob_match_local(args[1], key)) out.push_back(Reply::bulk(key));
    }
    return res(Reply::array(std::move(out)));
  });
  reg(table, "SCAN", -2, kReadOnly, [](Client& client, Args& args) -> CommandResult {
    uint64_t cursor = static_cast<uint64_t>(parse_int(args[1]));
    std::string pattern = "*";
    int64_t count = 10;
    for (size_t i = 2; i + 1 < args.size(); i += 2) {
      std::string option = upper_ascii(args[i]);
      if (option == "MATCH") pattern = args[i + 1];
      else if (option == "COUNT") count = parse_int(args[i + 1]);
      else if (option == "TYPE") { /* filter applied below */ }
      else return res(err_syntax());
    }
    if (count < 1) return res(err_syntax());
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    auto keys = live_keys(client.srv.store, client.srv.store.now_ms());
    std::vector<Reply> page;
    size_t examined = 0;
    while (cursor < keys.size() && examined < static_cast<size_t>(count)) {
      if (glob_match_local(pattern, keys[cursor])) page.push_back(Reply::bulk(keys[cursor]));
      ++cursor;
      ++examined;
    }
    if (cursor >= keys.size()) cursor = 0;
    return res(Reply::array({Reply::bulk(std::to_string(cursor)), Reply::array(std::move(page))}));
  });
  reg(table, "RANDOMKEY", 1, kReadOnly, [](Client& client, Args&) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    auto keys = live_keys(client.srv.store, client.srv.store.now_ms());
    if (keys.empty()) return res(Reply::null());
    return res(Reply::bulk(keys.front()));
  });
  reg(table, "RENAME", 3, kWrite, [](Client& client, Args& args) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    int64_t now = client.srv.store.now_ms();
    Entry* source = client.srv.store.lookup_write(args[1], now);
    if (!source) return res(Reply::error("ERR no such key"));
    Entry moved = std::move(*source);
    client.srv.store.data.erase(args[1]);
    client.srv.store.data[args[2]] = std::move(moved);
    client.srv.store.touch(client.srv.store.data[args[2]]);
    return res(ok_reply(), true);
  });
  reg(table, "RENAMENX", 3, kWrite, [](Client& client, Args& args) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    int64_t now = client.srv.store.now_ms();
    Entry* source = client.srv.store.lookup_write(args[1], now);
    if (!source) return res(Reply::error("ERR no such key"));
    if (client.srv.store.lookup_read(args[2], now)) return res(int_reply(0));
    Entry moved = std::move(*source);
    client.srv.store.data.erase(args[1]);
    client.srv.store.data[args[2]] = std::move(moved);
    client.srv.store.touch(client.srv.store.data[args[2]]);
    return res(int_reply(1), true);
  });
  reg(table, "COPY", -3, kWrite, [](Client& client, Args& args) -> CommandResult {
    bool replace = false;
    for (size_t i = 3; i < args.size(); ++i) {
      if (upper_ascii(args[i]) == "REPLACE") replace = true;
      else return res(err_syntax());
    }
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    int64_t now = client.srv.store.now_ms();
    const Entry* source = client.srv.store.lookup_read(args[1], now);
    if (!source) return res(int_reply(0));
    if (!replace && client.srv.store.lookup_read(args[2], now)) return res(int_reply(0));
    client.srv.store.data[args[2]] = *source;
    client.srv.store.touch(client.srv.store.data[args[2]]);
    return res(int_reply(1), true);
  });

  auto expire_handler = [](Client& client, Args& args, bool absolute, int64_t unit) -> CommandResult {
    int64_t amount = parse_int(args[2]);
    std::string option = "NX";
    for (size_t i = 3; i < args.size(); ++i) {
      std::string next = upper_ascii(args[i]);
      if (next != "NX" && next != "XX" && next != "GT" && next != "LT") return res(err_syntax());
      option = next;
    }
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    int64_t now = client.srv.store.now_ms();
    Entry* entry = client.srv.store.lookup_write(args[1], now);
    if (!entry) return res(int_reply(0));
    int64_t at = absolute ? amount * unit : now + amount * unit;
    int64_t current = entry->expire_at;
    bool ok = true;
    if (option == "NX") ok = current == 0;
    else if (option == "XX") ok = current != 0;
    else if (option == "GT") ok = current != 0 && at > current;
    else if (option == "LT") ok = current == 0 || at < current;
    if (!ok) return res(int_reply(0));
    if (at <= now) {
      client.srv.store.data.erase(args[1]);
      ++client.srv.store.expired_keys;
      return res(int_reply(1), true, {"DEL", args[1]});
    }
    entry->expire_at = at;
    client.srv.store.touch(*entry);
    return res(int_reply(1), true, {"PEXPIREAT", args[1], std::to_string(at)});
  };
  reg(table, "EXPIRE", -3, kWrite | kFast, [expire_handler](Client& c, Args& a) {
    return expire_handler(c, a, false, 1000);
  });
  reg(table, "PEXPIRE", -3, kWrite | kFast, [expire_handler](Client& c, Args& a) {
    return expire_handler(c, a, false, 1);
  });
  reg(table, "EXPIREAT", -3, kWrite | kFast, [expire_handler](Client& c, Args& a) {
    return expire_handler(c, a, true, 1000);
  });
  reg(table, "PEXPIREAT", -3, kWrite | kFast, [expire_handler](Client& c, Args& a) {
    return expire_handler(c, a, true, 1);
  });

  reg(table, "TTL", 2, kReadOnly | kFast, [](Client& client, Args& args) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    int64_t now = client.srv.store.now_ms();
    const Entry* entry = client.srv.store.lookup_read(args[1], now);
    if (!entry) return res(int_reply(-2));
    if (entry->expire_at == 0) return res(int_reply(-1));
    return res(int_reply((entry->expire_at - now + 500) / 1000));
  });
  reg(table, "PTTL", 2, kReadOnly | kFast, [](Client& client, Args& args) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    int64_t now = client.srv.store.now_ms();
    const Entry* entry = client.srv.store.lookup_read(args[1], now);
    if (!entry) return res(int_reply(-2));
    if (entry->expire_at == 0) return res(int_reply(-1));
    return res(int_reply(entry->expire_at - now));
  });
  reg(table, "PERSIST", 2, kWrite | kFast, [](Client& client, Args& args) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    Entry* entry = client.srv.store.lookup_write(args[1], client.srv.store.now_ms());
    if (!entry || entry->expire_at == 0) return res(int_reply(0));
    entry->expire_at = 0;
    client.srv.store.touch(*entry);
    return res(int_reply(1), true);
  });
  reg(table, "EXPIRETIME", 2, kReadOnly | kFast, [](Client& client, Args& args) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    const Entry* entry = client.srv.store.lookup_read(args[1], client.srv.store.now_ms());
    if (!entry) return res(int_reply(-2));
    if (entry->expire_at == 0) return res(int_reply(-1));
    return res(int_reply(entry->expire_at / 1000));
  });
  reg(table, "PEXPIRETIME", 2, kReadOnly | kFast, [](Client& client, Args& args) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    const Entry* entry = client.srv.store.lookup_read(args[1], client.srv.store.now_ms());
    if (!entry) return res(int_reply(-2));
    if (entry->expire_at == 0) return res(int_reply(-1));
    return res(int_reply(entry->expire_at));
  });

  // ------------------------------------------------------------ transactions
  reg(table, "MULTI", 1, kFast, [](Client& client, Args&) -> CommandResult {
    if (client.multi) return res(Reply::error("ERR MULTI calls can not be nested"));
    client.multi = true;
    client.dirty = false;
    client.queued.clear();
    return res(ok_reply());
  });
  reg(table, "DISCARD", 1, kFast, [](Client& client, Args&) -> CommandResult {
    if (!client.multi) return res(Reply::error("ERR DISCARD without MULTI"));
    client.multi = false;
    client.queued.clear();
    client.watched.clear();
    client.dirty = false;
    return res(ok_reply());
  });
  reg(table, "WATCH", -2, kFast, [](Client& client, Args& args) -> CommandResult {
    if (client.multi) return res(Reply::error("ERR WATCH inside MULTI is not allowed"));
    for (size_t i = 1; i < args.size(); ++i) {
      client.watched[args[i]] = client.srv.store.watch(args[i]);
    }
    return res(ok_reply());
  });
  reg(table, "UNWATCH", 1, kFast, [](Client& client, Args&) -> CommandResult {
    client.watched.clear();
    return res(ok_reply());
  });
  reg(table, "EXEC", 1, kFast, [](Client& client, Args&) -> CommandResult {
    if (!client.multi) return res(Reply::error("ERR EXEC without MULTI"));
    if (client.dirty) {
      client.multi = false;
      client.queued.clear();
      client.watched.clear();
      client.dirty = false;
      return res(Reply::error("EXECABORT Transaction discarded because of previous errors."));
    }
    for (const auto& [key, state] : client.watched) {
      if (!(client.srv.store.watch(key) == state)) {
        client.multi = false;
        client.queued.clear();
        client.watched.clear();
        return res(Reply::null_array());
      }
    }
    auto queued = std::move(client.queued);
    client.queued.clear();
    client.multi = false;
    client.executing = true;
    client.watched.clear();
    std::vector<Reply> replies;
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    for (auto& command : queued) {
      try {
        CommandResult result = client.srv.execute_locked(client, command, true);
        replies.push_back(result.reply);
      } catch (const RedisError& error) {
        replies.push_back(Reply::error(error.what()));
      } catch (const std::exception& error) {
        replies.push_back(Reply::error(std::string("ERR ") + error.what()));
      }
    }
    client.executing = false;
    return res(Reply::array(std::move(replies)));
  });

  // ------------------------------------------------------------ pub/sub
  auto subscribe_handler = [](Client& client, Args& args, bool pattern) -> CommandResult {
    std::vector<Reply> out;
    for (size_t i = 1; i < args.size(); ++i) {
      if (pattern) client.srv.psubscribe(client, args[i]);
      else client.srv.subscribe(client, args[i]);
      size_t count = client.subs.size() + client.psubs.size() + client.ssubs.size();
      out.push_back(Reply::push({Reply::bulk(pattern ? "psubscribe" : "subscribe"),
                                 Reply::bulk(args[i]), int_reply(static_cast<int64_t>(count))}));
    }
    return res(Reply::multi(std::move(out)));
  };
  auto unsubscribe_handler = [](Client& client, Args& args, bool pattern) -> CommandResult {
    std::vector<std::string> names;
    if (args.size() == 1) {
      const auto& source = pattern ? client.psubs : client.subs;
      names.assign(source.begin(), source.end());
    } else {
      names.assign(args.begin() + 1, args.end());
    }
    std::vector<Reply> out;
    if (names.empty()) {
      out.push_back(Reply::push({Reply::bulk(pattern ? "punsubscribe" : "unsubscribe"),
                                 Reply::null(), int_reply(0)}));
      return res(Reply::multi(std::move(out)));
    }
    for (const auto& name : names) {
      if (pattern) client.srv.punsubscribe(client, name);
      else client.srv.unsubscribe(client, name);
      size_t count = client.subs.size() + client.psubs.size() + client.ssubs.size();
      out.push_back(Reply::push({Reply::bulk(pattern ? "punsubscribe" : "unsubscribe"),
                                 Reply::bulk(name), int_reply(static_cast<int64_t>(count))}));
    }
    return res(Reply::multi(std::move(out)));
  };
  reg(table, "SUBSCRIBE", -2, kFast, [subscribe_handler](Client& c, Args& a) {
    return subscribe_handler(c, a, false);
  });
  reg(table, "PSUBSCRIBE", -2, kFast, [subscribe_handler](Client& c, Args& a) {
    return subscribe_handler(c, a, true);
  });
  reg(table, "UNSUBSCRIBE", -1, kFast, [unsubscribe_handler](Client& c, Args& a) {
    return unsubscribe_handler(c, a, false);
  });
  reg(table, "PUNSUBSCRIBE", -1, kFast, [unsubscribe_handler](Client& c, Args& a) {
    return unsubscribe_handler(c, a, true);
  });
  reg(table, "PUBLISH", 3, kFast, [](Client& client, Args& args) -> CommandResult {
    return res(int_reply(client.srv.publish(args[1], args[2])));
  });
  reg(table, "SSUBSCRIBE", -2, kFast, [](Client& client, Args& args) -> CommandResult {
    std::vector<Reply> out;
    for (size_t i = 1; i < args.size(); ++i) {
      client.srv.ssubscribe(client, args[i]);
      size_t count = client.subs.size() + client.psubs.size() + client.ssubs.size();
      out.push_back(Reply::push({Reply::bulk("ssubscribe"), Reply::bulk(args[i]),
                                 int_reply(static_cast<int64_t>(count))}));
    }
    return res(Reply::multi(std::move(out)));
  });
  reg(table, "SUNSUBSCRIBE", -1, kFast, [](Client& client, Args& args) -> CommandResult {
    std::vector<std::string> names;
    if (args.size() == 1) names.assign(client.ssubs.begin(), client.ssubs.end());
    else names.assign(args.begin() + 1, args.end());
    std::vector<Reply> out;
    if (names.empty()) {
      out.push_back(Reply::push({Reply::bulk("sunsubscribe"), Reply::null(), int_reply(0)}));
      return res(Reply::multi(std::move(out)));
    }
    for (const auto& name : names) {
      client.srv.sunsubscribe(client, name);
      size_t count = client.subs.size() + client.psubs.size() + client.ssubs.size();
      out.push_back(Reply::push({Reply::bulk("sunsubscribe"), Reply::bulk(name),
                                 int_reply(static_cast<int64_t>(count))}));
    }
    return res(Reply::multi(std::move(out)));
  });
  reg(table, "SPUBLISH", 3, kFast, [](Client& client, Args& args) -> CommandResult {
    return res(int_reply(client.srv.publish_shard(args[1], args[2])));
  });
  reg(table, "PUBSUB", -2, kFast, [](Client& client, Args& args) -> CommandResult {
    std::string sub = upper_ascii(args[1]);
    if (sub == "CHANNELS") {
      std::string pattern;
      if (args.size() == 3) pattern = args[2];
      std::vector<Reply> out;
      for (const auto& name : client.srv.channel_names(pattern)) out.push_back(Reply::bulk(name));
      return res(Reply::array(std::move(out)));
    }
    if (sub == "NUMSUB") {
      std::vector<Reply> out;
      for (size_t i = 2; i < args.size(); ++i) {
        out.push_back(Reply::bulk(args[i]));
        out.push_back(int_reply(client.srv.subscriber_count(args[i])));
      }
      return res(Reply::array(std::move(out)));
    }
    if (sub == "NUMPAT") return res(int_reply(client.srv.pattern_count()));
    if (sub == "SHARDCHANNELS") {
      std::string pattern;
      if (args.size() == 3) pattern = args[2];
      std::vector<Reply> out;
      for (const auto& name : client.srv.shard_channel_names(pattern)) out.push_back(Reply::bulk(name));
      return res(Reply::array(std::move(out)));
    }
    if (sub == "SHARDNUMSUB") {
      std::vector<Reply> out;
      for (size_t i = 2; i < args.size(); ++i) {
        out.push_back(Reply::bulk(args[i]));
        out.push_back(int_reply(client.srv.shard_subscriber_count(args[i])));
      }
      return res(Reply::array(std::move(out)));
    }
    return res(Reply::error("ERR unknown subcommand '" + args[1] + "' for PUBSUB"));
  });

  // ------------------------------------------------------------ bitmaps
  reg(table, "SETBIT", 4, kWrite, [](Client& client, Args& args) -> CommandResult {
    int64_t offset = parse_int(args[2]);
    int64_t bit = parse_int(args[3]);
    if (offset < 0 || offset >= 512LL * 1024 * 1024 * 8) {
      return res(Reply::error("ERR bit offset is not an integer or out of range"));
    }
    if (bit != 0 && bit != 1) return res(Reply::error("ERR bit is not an integer or out of range"));
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    int64_t now = client.srv.store.now_ms();
    Entry* entry = client.srv.store.lookup_write(args[1], now);
    if (!entry) {
      entry = &client.srv.store.create(args[1]);
      entry->value = String{};
    }
    String& value = value_as<String>(entry->value);
    size_t index = static_cast<size_t>(offset / 8);
    if (index >= value.size()) value.resize(index + 1, '\0');
    unsigned char mask = static_cast<unsigned char>(1u << (7 - (offset % 8)));
    int old = (static_cast<unsigned char>(value[index]) & mask) ? 1 : 0;
    if (bit) value[index] = static_cast<char>(static_cast<unsigned char>(value[index]) | mask);
    else value[index] = static_cast<char>(static_cast<unsigned char>(value[index]) & ~mask);
    client.srv.store.touch(*entry);
    return res(int_reply(old), true);
  });
  reg(table, "GETBIT", 3, kReadOnly | kFast, [](Client& client, Args& args) -> CommandResult {
    int64_t offset = parse_int(args[2]);
    if (offset < 0) return res(Reply::error("ERR bit offset is not an integer or out of range"));
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    const Entry* entry = client.srv.store.lookup_read(args[1], client.srv.store.now_ms());
    if (!entry) return res(int_reply(0));
    const String& value = value_as<String>(entry->value);
    size_t index = static_cast<size_t>(offset / 8);
    if (index >= value.size()) return res(int_reply(0));
    unsigned char mask = static_cast<unsigned char>(1u << (7 - (offset % 8)));
    return res(int_reply((static_cast<unsigned char>(value[index]) & mask) ? 1 : 0));
  });
  reg(table, "BITCOUNT", -2, kReadOnly, [](Client& client, Args& args) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    const Entry* entry = client.srv.store.lookup_read(args[1], client.srv.store.now_ms());
    if (!entry) return res(int_reply(0));
    const String& value = value_as<String>(entry->value);
    int64_t start = 0;
    int64_t end = static_cast<int64_t>(value.size()) - 1;
    if (args.size() >= 4) {
      start = parse_int(args[2]);
      end = parse_int(args[3]);
    }
    if (start < 0) start += static_cast<int64_t>(value.size());
    if (end < 0) end += static_cast<int64_t>(value.size());
    if (start < 0) start = 0;
    if (end >= static_cast<int64_t>(value.size())) end = static_cast<int64_t>(value.size()) - 1;
    if (start > end || value.empty()) return res(int_reply(0));
    int64_t count = 0;
    for (int64_t i = start; i <= end; ++i) {
      count += __builtin_popcount(static_cast<unsigned char>(value[static_cast<size_t>(i)]));
    }
    return res(int_reply(count));
  });

  // ------------------------------------------------------------ misc
  reg(table, "OBJECT", -2, kReadOnly, [](Client& client, Args& args) -> CommandResult {
    if (args.size() < 3) return res(err_syntax());
    std::string sub = upper_ascii(args[1]);
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    const Entry* entry = client.srv.store.lookup_read(args[2], client.srv.store.now_ms());
    if (!entry) return res(Reply::null());
    if (sub == "ENCODING") {
      switch (value_type(entry->value)) {
        case ValueType::String: return res(Reply::bulk("raw"));
        case ValueType::Hash: return res(Reply::bulk("hashtable"));
        case ValueType::List: return res(Reply::bulk("quicklist"));
        case ValueType::Set: return res(Reply::bulk("hashtable"));
        case ValueType::ZSet: return res(Reply::bulk("skiplist"));
      }
    }
    if (sub == "REFCOUNT") return res(int_reply(1));
    if (sub == "IDLETIME" || sub == "FREQ") return res(int_reply(0));
    return res(Reply::error("ERR unknown subcommand '" + args[1] + "' for OBJECT"));
  });
  reg(table, "MEMORY", -2, kReadOnly, [](Client& client, Args& args) -> CommandResult {
    std::string sub = upper_ascii(args[1]);
    if (sub == "USAGE") {
      if (args.size() < 3) return res(err_syntax());
      std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
      const Entry* entry = client.srv.store.lookup_read(args[2], client.srv.store.now_ms());
      if (!entry) return res(Reply::null());
      return res(int_reply(64 + static_cast<int64_t>(args[2].size())));
    }
    if (sub == "DOCTOR") return res(Reply::bulk("Sam Redlite looks fine."));
    if (sub == "STATS") return res(Reply::array({}));
    if (sub == "PURGE") return res(ok_reply());
    return res(Reply::error("ERR unknown subcommand '" + args[1] + "' for MEMORY"));
  });
  reg(table, "LOLWUT", -1, kReadOnly, [](Client&, Args&) -> CommandResult {
    return res(Reply::bulk("redlite cpp\nOne shard to rule them all."));
  });
}

}  // namespace redlite
