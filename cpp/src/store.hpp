#pragma once

#include <atomic>
#include <condition_variable>
#include <cstdint>
#include <deque>
#include <mutex>
#include <optional>
#include <set>
#include <stdexcept>
#include <string>
#include <unordered_map>
#include <unordered_set>
#include <variant>
#include <vector>

namespace redlite {

struct RedisError : std::runtime_error {
  explicit RedisError(const std::string& message) : std::runtime_error(message) {}
};

struct ZSet {
  std::set<std::pair<double, std::string>> order;  // (score, member)
  std::unordered_map<std::string, double> scores;

  void insert(const std::string& member, double score);
  bool erase(const std::string& member);
  bool has(const std::string& member) const;
  size_t size() const { return scores.size(); }
  std::vector<std::pair<std::string, double>> ordered() const;
  std::vector<std::pair<std::string, double>> reversed() const;
};

using String = std::string;
using Hash = std::unordered_map<std::string, std::string>;
using List = std::deque<std::string>;
using Set = std::unordered_set<std::string>;
using Value = std::variant<String, Hash, List, Set, ZSet>;

enum class ValueType { String, Hash, List, Set, ZSet };
ValueType value_type(const Value& value);
const char* type_name(ValueType type);

struct Entry {
  Value value;
  int64_t expire_at = 0;  // unix milliseconds, 0 = none
  uint64_t version = 0;
};

struct WatchState {
  uint64_t version = 0;
  bool exists = false;
  uint64_t epoch = 0;

  bool operator==(const WatchState& other) const {
    return version == other.version && exists == other.exists && epoch == other.epoch;
  }
};

class Store {
 public:
  Store() = default;
  Store(const Store&) = delete;
  Store& operator=(const Store&) = delete;

  int64_t now_ms() const;
  static bool expired(const Entry& entry, int64_t now) {
    return entry.expire_at != 0 && entry.expire_at <= now;
  }

  // Callers must hold mu.
  Entry* lookup_write(const std::string& key, int64_t now);
  const Entry* lookup_read(const std::string& key, int64_t now) const;
  Entry& create(const std::string& key);

  WatchState watch(const std::string& key);
  bool del(const std::string& key);  // caller holds mu
  void flush();

  size_t size() const;
  size_t expiring() const;

  mutable std::recursive_mutex mu;
  std::mutex wait_mu;
  std::condition_variable cv;
  std::unordered_map<std::string, Entry> data;
  uint64_t next_version = 1;
  uint64_t epoch = 1;
  std::atomic<uint64_t> signal_seq{0};

  uint64_t touch(Entry& entry) { return entry.version = next_version++; }
  void signal() {
    signal_seq.fetch_add(1);
    cv.notify_all();
  }

  // Counters reported by INFO.
  uint64_t hits = 0;
  uint64_t misses = 0;
  uint64_t expired_keys = 0;
  uint64_t evicted_keys = 0;

};

// Helpers used by command handlers while holding Store::mu.
template <typename T>
T& value_as(Value& value) {
  if (auto* ptr = std::get_if<T>(&value)) return *ptr;
  throw RedisError("WRONGTYPE Operation against a key holding the wrong kind of value");
}

template <typename T>
const T& value_as(const Value& value) {
  if (const auto* ptr = std::get_if<T>(&value)) return *ptr;
  throw RedisError("WRONGTYPE Operation against a key holding the wrong kind of value");
}

inline bool is_wrong_type(const RedisError& error) {
  return std::string(error.what()).rfind("WRONGTYPE", 0) == 0;
}

std::vector<std::string> sorted_keys(const Hash& hash);
std::vector<std::string> sorted_members(const Set& set);

}  // namespace redlite
