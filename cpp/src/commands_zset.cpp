#include "server.hpp"

#include <algorithm>
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

struct ScoreBound {
  double value;
  bool exclusive;
};

ScoreBound parse_bound(const std::string& text) {
  ScoreBound bound{0, false};
  std::string raw = text;
  if (!raw.empty() && raw[0] == '(') {
    bound.exclusive = true;
    raw = raw.substr(1);
  }
  if (raw == "-inf") bound.value = -std::numeric_limits<double>::infinity();
  else if (raw == "+inf" || raw == "inf") bound.value = std::numeric_limits<double>::infinity();
  else bound.value = parse_float(raw);
  return bound;
}

bool above_min(double score, const ScoreBound& min) {
  return min.exclusive ? score > min.value : score >= min.value;
}
bool below_max(double score, const ScoreBound& max) {
  return max.exclusive ? score < max.value : score <= max.value;
}

Reply entries_reply(const std::vector<std::pair<std::string, double>>& entries,
                    bool with_scores) {
  if (!with_scores) {
    std::vector<Reply> out;
    for (const auto& [member, _] : entries) out.push_back(Reply::bulk(member));
    return Reply::array(std::move(out));
  }
  std::vector<Reply> flat;
  for (const auto& [member, score] : entries) {
    flat.push_back(Reply::bulk(member));
    flat.push_back(Reply::double_reply(score));
  }
  return Reply::pair_array(std::move(flat));
}
}  // namespace

void register_zset_commands(CommandMap& table) {
  reg(table, "ZADD", -4, kWrite, [](Client& client, Args& args) -> CommandResult {
    bool nx = false, xx = false, gt = false, lt = false, ch = false, incr = false;
    size_t index = 2;
    while (index < args.size()) {
      std::string option = upper_ascii(args[index]);
      if (option == "NX") nx = true;
      else if (option == "XX") xx = true;
      else if (option == "GT") gt = true;
      else if (option == "LT") lt = true;
      else if (option == "CH") ch = true;
      else if (option == "INCR") incr = true;
      else break;
      ++index;
    }
    if ((nx && xx) || (gt && lt) || (nx && (gt || lt))) {
      return res(Reply::error("ERR XX and NX options at the same time are not compatible"));
    }
    if (index >= args.size() || (args.size() - index) % 2 != 0) return res(Reply::error("ERR syntax error"));
    if (incr && args.size() - index != 2) {
      return res(Reply::error("ERR INCR option supports a single increment-element pair"));
    }
    std::vector<std::pair<double, std::string>> pairs;
    for (size_t i = index; i + 1 < args.size(); i += 2) {
      pairs.push_back({parse_float(args[i]), args[i + 1]});
    }
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    Entry* entry = client.srv.store.lookup_write(args[1], client.srv.store.now_ms());
    if (!entry) {
      if (xx) return res(incr ? Reply::null() : Reply::integer_reply(0));
      entry = &client.srv.store.create(args[1]);
      entry->value = ZSet{};
    }
    if (value_type(entry->value) != ValueType::ZSet) return res(wrongtype());
    ZSet& zset = std::get<ZSet>(entry->value);
    int64_t added = 0, changed = 0;
    double last = 0;
    bool has_last = false;
    for (auto [score, member] : pairs) {
      auto it = zset.scores.find(member);
      bool exists = it != zset.scores.end();
      if (incr) {
        if (exists) {
          if (nx) return res(Reply::null());
          score = it->second + score;
        } else if (xx) {
          return res(Reply::null());
        }
        if (std::isnan(score)) return res(Reply::error("ERR resulting score is not a number (NaN)"));
      }
      if (exists) {
        if (nx) continue;
        if (gt && score <= it->second) continue;
        if (lt && score >= it->second) continue;
        if (score == it->second) {
          if (incr) {
            last = score;
            has_last = true;
          }
          continue;
        }
        zset.insert(member, score);
        ++changed;
      } else {
        if (xx) continue;
        zset.insert(member, score);
        ++added;
        ++changed;
      }
      if (incr) {
        last = score;
        has_last = true;
      }
    }
    if (changed > 0) client.srv.store.touch(*entry);
    if (incr) {
      if (!has_last) return res(Reply::null());
      return res(Reply::double_reply(last), true);
    }
    int64_t result = ch ? changed : added;
    return res(Reply::integer_reply(result), changed > 0);
  });

  reg(table, "ZREM", -3, kWrite | kFast, [](Client& client, Args& args) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    Entry* entry = client.srv.store.lookup_write(args[1], client.srv.store.now_ms());
    if (!entry) return res(Reply::integer_reply(0));
    if (value_type(entry->value) != ValueType::ZSet) return res(wrongtype());
    ZSet& zset = std::get<ZSet>(entry->value);
    int64_t removed = 0;
    for (size_t i = 2; i < args.size(); ++i) removed += zset.erase(args[i]);
    if (zset.size() == 0) client.srv.store.data.erase(args[1]);
    else if (removed > 0) client.srv.store.touch(*entry);
    return res(Reply::integer_reply(removed), removed > 0);
  });

  reg(table, "ZSCORE", 3, kReadOnly | kFast, [](Client& client, Args& args) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    const Entry* entry = client.srv.store.lookup_read(args[1], client.srv.store.now_ms());
    if (!entry) return res(Reply::null());
    if (value_type(entry->value) != ValueType::ZSet) return res(wrongtype());
    const ZSet& zset = std::get<ZSet>(entry->value);
    auto it = zset.scores.find(args[2]);
    if (it == zset.scores.end()) return res(Reply::null());
    return res(Reply::double_reply(it->second));
  });
  reg(table, "ZMSCORE", -3, kReadOnly | kFast, [](Client& client, Args& args) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    const Entry* entry = client.srv.store.lookup_read(args[1], client.srv.store.now_ms());
    if (entry && value_type(entry->value) != ValueType::ZSet) return res(wrongtype());
    const ZSet* zset = entry ? &std::get<ZSet>(entry->value) : nullptr;
    std::vector<Reply> out;
    for (size_t i = 2; i < args.size(); ++i) {
      auto it = zset ? zset->scores.find(args[i]) : std::unordered_map<std::string, double>::const_iterator{};
      if (!zset || it == zset->scores.end()) out.push_back(Reply::null());
      else out.push_back(Reply::double_reply(it->second));
    }
    return res(Reply::array(std::move(out)));
  });
  reg(table, "ZINCRBY", 4, kWrite | kFast, [](Client& client, Args& args) -> CommandResult {
    double delta = parse_float(args[2]);
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    Entry* entry = client.srv.store.lookup_write(args[1], client.srv.store.now_ms());
    if (!entry) {
      entry = &client.srv.store.create(args[1]);
      entry->value = ZSet{};
    }
    if (value_type(entry->value) != ValueType::ZSet) return res(wrongtype());
    ZSet& zset = std::get<ZSet>(entry->value);
    double score = zset.scores.count(args[3]) ? zset.scores[args[3]] + delta : delta;
    if (std::isnan(score)) return res(Reply::error("ERR resulting score is not a number (NaN)"));
    zset.insert(args[3], score);
    client.srv.store.touch(*entry);
    return res(Reply::double_reply(score), true);
  });
  reg(table, "ZCARD", 2, kReadOnly | kFast, [](Client& client, Args& args) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    const Entry* entry = client.srv.store.lookup_read(args[1], client.srv.store.now_ms());
    if (!entry) return res(Reply::integer_reply(0));
    if (value_type(entry->value) != ValueType::ZSet) return res(wrongtype());
    return res(Reply::integer_reply(static_cast<int64_t>(std::get<ZSet>(entry->value).size())));
  });
  reg(table, "ZCOUNT", 4, kReadOnly, [](Client& client, Args& args) -> CommandResult {
    ScoreBound min = parse_bound(args[2]);
    ScoreBound max = parse_bound(args[3]);
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    const Entry* entry = client.srv.store.lookup_read(args[1], client.srv.store.now_ms());
    if (!entry) return res(Reply::integer_reply(0));
    if (value_type(entry->value) != ValueType::ZSet) return res(wrongtype());
    int64_t count = 0;
    for (const auto& [score, member] : std::get<ZSet>(entry->value).order) {
      (void)member;
      if (above_min(score, min) && below_max(score, max)) ++count;
    }
    return res(Reply::integer_reply(count));
  });

  auto range_rank = [](Client& client, Args& args, bool reverse) -> CommandResult {
    int64_t start = parse_int(args[2]);
    int64_t stop = parse_int(args[3]);
    bool with_scores = false;
    for (size_t i = 4; i < args.size(); ++i) {
      if (upper_ascii(args[i]) == "WITHSCORES") with_scores = true;
      else return res(Reply::error("ERR syntax error"));
    }
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    const Entry* entry = client.srv.store.lookup_read(args[1], client.srv.store.now_ms());
    if (!entry) return res(Reply::array({}));
    if (value_type(entry->value) != ValueType::ZSet) return res(wrongtype());
    const ZSet& zset = std::get<ZSet>(entry->value);
    std::vector<std::pair<std::string, double>> entries;
    for (const auto& [score, member] : zset.order) entries.push_back({member, score});
    if (reverse) std::reverse(entries.begin(), entries.end());
    int64_t size = static_cast<int64_t>(entries.size());
    if (start < 0) start += size;
    if (stop < 0) stop += size;
    if (start < 0) start = 0;
    if (stop >= size) stop = size - 1;
    std::vector<std::pair<std::string, double>> selected;
    for (int64_t i = start; i <= stop && i < size; ++i) {
      if (i >= 0) selected.push_back(entries[static_cast<size_t>(i)]);
    }
    return res(entries_reply(selected, with_scores));
  };
  reg(table, "ZRANGE", -4, kReadOnly, [range_rank](Client& c, Args& a) {
    return range_rank(c, a, false);
  });
  reg(table, "ZREVRANGE", -4, kReadOnly, [range_rank](Client& c, Args& a) {
    return range_rank(c, a, true);
  });

  auto range_score = [](Client& client, Args& args, bool reverse) -> CommandResult {
    ScoreBound min, max;
    try {
      min = parse_bound(reverse ? args[3] : args[2]);
      max = parse_bound(reverse ? args[2] : args[3]);
    } catch (const RedisError&) {
      return res(Reply::error("ERR min or max is not a float"));
    }
    bool with_scores = false;
    int64_t offset = 0, count = -1;
    for (size_t i = 4; i < args.size(); ++i) {
      std::string option = upper_ascii(args[i]);
      if (option == "WITHSCORES") with_scores = true;
      else if (option == "LIMIT" && i + 2 < args.size()) {
        offset = parse_int(args[++i]);
        count = parse_int(args[++i]);
      } else {
        return res(Reply::error("ERR syntax error"));
      }
    }
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    const Entry* entry = client.srv.store.lookup_read(args[1], client.srv.store.now_ms());
    if (!entry) return res(Reply::array({}));
    if (value_type(entry->value) != ValueType::ZSet) return res(wrongtype());
    const ZSet& zset = std::get<ZSet>(entry->value);
    std::vector<std::pair<std::string, double>> entries;
    for (const auto& [score, member] : zset.order) {
      if (above_min(score, min) && below_max(score, max)) entries.push_back({member, score});
    }
    if (reverse) std::reverse(entries.begin(), entries.end());
    if (offset < 0) return res(Reply::array({}));
    std::vector<std::pair<std::string, double>> selected;
    for (size_t i = static_cast<size_t>(offset); i < entries.size(); ++i) {
      if (count >= 0 && static_cast<int64_t>(selected.size()) >= count) break;
      selected.push_back(entries[i]);
    }
    return res(entries_reply(selected, with_scores));
  };
  reg(table, "ZRANGEBYSCORE", -4, kReadOnly, [range_score](Client& c, Args& a) {
    return range_score(c, a, false);
  });
  reg(table, "ZREVRANGEBYSCORE", -4, kReadOnly, [range_score](Client& c, Args& a) {
    return range_score(c, a, true);
  });

  reg(table, "ZRANK", 3, kReadOnly | kFast, [](Client& client, Args& args) -> CommandResult {
    bool reverse = upper_ascii(args[0]) == "ZREVRANK";
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    const Entry* entry = client.srv.store.lookup_read(args[1], client.srv.store.now_ms());
    if (!entry) return res(Reply::null());
    if (value_type(entry->value) != ValueType::ZSet) return res(wrongtype());
    const ZSet& zset = std::get<ZSet>(entry->value);
    auto it = zset.scores.find(args[2]);
    if (it == zset.scores.end()) return res(Reply::null());
    int64_t rank = 0;
    for (const auto& [score, member] : zset.order) {
      if (member == args[2]) break;
      ++rank;
    }
    if (reverse) rank = static_cast<int64_t>(zset.size()) - 1 - rank;
    return res(Reply::integer_reply(rank));
  });
  reg(table, "ZREVRANK", 3, kReadOnly | kFast, table.at("ZRANK").handler);

  reg(table, "ZREMRANGEBYRANK", 4, kWrite, [](Client& client, Args& args) -> CommandResult {
    int64_t start = parse_int(args[2]);
    int64_t stop = parse_int(args[3]);
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    Entry* entry = client.srv.store.lookup_write(args[1], client.srv.store.now_ms());
    if (!entry) return res(Reply::integer_reply(0));
    if (value_type(entry->value) != ValueType::ZSet) return res(wrongtype());
    ZSet& zset = std::get<ZSet>(entry->value);
    std::vector<std::pair<std::string, double>> entries;
    for (const auto& [score, member] : zset.order) entries.push_back({member, score});
    int64_t size = static_cast<int64_t>(entries.size());
    if (start < 0) start += size;
    if (stop < 0) stop += size;
    if (start < 0) start = 0;
    if (stop >= size) stop = size - 1;
    std::vector<std::string> members;
    for (int64_t i = start; i <= stop && i < size; ++i) {
      if (i >= 0) members.push_back(entries[static_cast<size_t>(i)].first);
    }
    for (const auto& member : members) zset.erase(member);
    if (zset.size() == 0) client.srv.store.data.erase(args[1]);
    else if (!members.empty()) client.srv.store.touch(*entry);
    return res(Reply::integer_reply(static_cast<int64_t>(members.size())), !members.empty());
  });
  reg(table, "ZREMRANGEBYSCORE", 4, kWrite, [](Client& client, Args& args) -> CommandResult {
    ScoreBound min, max;
    try {
      min = parse_bound(args[2]);
      max = parse_bound(args[3]);
    } catch (const RedisError&) {
      return res(Reply::error("ERR min or max is not a float"));
    }
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    Entry* entry = client.srv.store.lookup_write(args[1], client.srv.store.now_ms());
    if (!entry) return res(Reply::integer_reply(0));
    if (value_type(entry->value) != ValueType::ZSet) return res(wrongtype());
    ZSet& zset = std::get<ZSet>(entry->value);
    std::vector<std::string> members;
    for (const auto& [score, member] : zset.order) {
      if (above_min(score, min) && below_max(score, max)) members.push_back(member);
    }
    for (const auto& member : members) zset.erase(member);
    if (zset.size() == 0) client.srv.store.data.erase(args[1]);
    else if (!members.empty()) client.srv.store.touch(*entry);
    return res(Reply::integer_reply(static_cast<int64_t>(members.size())), !members.empty());
  });

  reg(table, "ZPOPMIN", -2, kWrite | kFast, [](Client& client, Args& args) -> CommandResult {
    int64_t count = args.size() == 3 ? parse_int(args[2]) : 1;
    bool min = upper_ascii(args[0]) == "ZPOPMIN";
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    Entry* entry = client.srv.store.lookup_write(args[1], client.srv.store.now_ms());
    if (!entry) return res(Reply::array({}));
    if (value_type(entry->value) != ValueType::ZSet) return res(wrongtype());
    ZSet& zset = std::get<ZSet>(entry->value);
    std::vector<std::pair<std::string, double>> out;
    for (int64_t i = 0; i < count && zset.size() > 0; ++i) {
      auto it = min ? zset.order.begin() : std::prev(zset.order.end());
      out.push_back({it->second, it->first});
      zset.erase(it->second);
    }
    if (zset.size() == 0) client.srv.store.data.erase(args[1]);
    else if (!out.empty()) client.srv.store.touch(*entry);
    std::vector<Reply> flat;
    for (const auto& [member, score] : out) {
      flat.push_back(Reply::bulk(member));
      flat.push_back(Reply::double_reply(score));
    }
    return res(Reply::pair_array(std::move(flat)), !out.empty());
  });
  reg(table, "ZPOPMAX", -2, kWrite | kFast, table.at("ZPOPMIN").handler);

  reg(table, "ZRANDMEMBER", -2, kReadOnly, [](Client& client, Args& args) -> CommandResult {
    std::lock_guard<std::recursive_mutex> lock(client.srv.store.mu);
    const Entry* entry = client.srv.store.lookup_read(args[1], client.srv.store.now_ms());
    if (!entry) return res(args.size() == 2 ? Reply::null() : Reply::array({}));
    if (value_type(entry->value) != ValueType::ZSet) return res(wrongtype());
    auto entries = std::get<ZSet>(entry->value).ordered();
    if (args.size() == 2) {
      if (entries.empty()) return res(Reply::null());
      return res(Reply::bulk(entries.front().first));
    }
    int64_t count = parse_int(args[2]);
    bool with_scores = args.size() == 4 && upper_ascii(args[3]) == "WITHSCORES";
    std::vector<std::pair<std::string, double>> selected;
    if (count < 0) {
      count = -count;
      for (int64_t i = 0; i < count && !entries.empty(); ++i) {
        selected.push_back(entries[static_cast<size_t>(i) % entries.size()]);
      }
    } else {
      for (int64_t i = 0; i < count && i < static_cast<int64_t>(entries.size()); ++i) {
        selected.push_back(entries[static_cast<size_t>(i)]);
      }
    }
    return res(entries_reply(selected, with_scores));
  });

  reg(table, "ZSCAN", -3, kReadOnly, [](Client& client, Args& args) -> CommandResult {
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
    if (value_type(entry->value) != ValueType::ZSet) return res(wrongtype());
    auto entries = std::get<ZSet>(entry->value).ordered();
    std::vector<Reply> page;
    size_t examined = 0;
    while (cursor < entries.size() && examined < static_cast<size_t>(count)) {
      if (glob_match_local(pattern, entries[cursor].first)) {
        page.push_back(Reply::bulk(entries[cursor].first));
        page.push_back(Reply::bulk(format_score(entries[cursor].second)));
      }
      ++cursor;
      ++examined;
    }
    if (cursor >= entries.size()) cursor = 0;
    return res(Reply::array({Reply::bulk(std::to_string(cursor)), Reply::array(std::move(page))}));
  });
}

}  // namespace redlite
