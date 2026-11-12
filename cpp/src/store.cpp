#include "store.hpp"

#include <algorithm>
#include <chrono>

namespace redlite {

void ZSet::insert(const std::string& member, double score) {
  auto it = scores.find(member);
  if (it != scores.end()) {
    order.erase({it->second, member});
    it->second = score;
  } else {
    scores.emplace(member, score);
  }
  order.emplace(score, member);
}

bool ZSet::erase(const std::string& member) {
  auto it = scores.find(member);
  if (it == scores.end()) return false;
  order.erase({it->second, member});
  scores.erase(it);
  return true;
}

bool ZSet::has(const std::string& member) const { return scores.find(member) != scores.end(); }

std::vector<std::pair<std::string, double>> ZSet::ordered() const {
  std::vector<std::pair<std::string, double>> out;
  out.reserve(order.size());
  for (const auto& item : order) out.emplace_back(item.second, item.first);
  return out;
}

std::vector<std::pair<std::string, double>> ZSet::reversed() const {
  std::vector<std::pair<std::string, double>> out;
  out.reserve(order.size());
  for (auto it = order.rbegin(); it != order.rend(); ++it) out.emplace_back(it->second, it->first);
  return out;
}

ValueType value_type(const Value& value) {
  switch (value.index()) {
    case 0: return ValueType::String;
    case 1: return ValueType::Hash;
    case 2: return ValueType::List;
    case 3: return ValueType::Set;
    default: return ValueType::ZSet;
  }
}

const char* type_name(ValueType type) {
  switch (type) {
    case ValueType::String: return "string";
    case ValueType::Hash: return "hash";
    case ValueType::List: return "list";
    case ValueType::Set: return "set";
    case ValueType::ZSet: return "zset";
  }
  return "none";
}

int64_t Store::now_ms() const {
  using namespace std::chrono;
  return duration_cast<milliseconds>(system_clock::now().time_since_epoch()).count();
}

Entry* Store::lookup_write(const std::string& key, int64_t now) {
  auto it = data.find(key);
  if (it == data.end()) return nullptr;
  if (expired(it->second, now)) {
    data.erase(it);
    ++expired_keys;
    cv.notify_all();
    return nullptr;
  }
  return &it->second;
}

const Entry* Store::lookup_read(const std::string& key, int64_t now) const {
  auto it = data.find(key);
  if (it == data.end()) return nullptr;
  if (expired(it->second, now)) return nullptr;
  return &it->second;
}

Entry& Store::create(const std::string& key) {
  Entry entry;
  entry.version = next_version++;
  auto [it, _] = data.emplace(key, std::move(entry));
  return it->second;
}

WatchState Store::watch(const std::string& key) {
  std::lock_guard<std::recursive_mutex> lock(mu);
  const Entry* entry = lookup_read(key, now_ms());
  if (entry == nullptr) return WatchState{0, false, epoch};
  return WatchState{entry->version, true, epoch};
}

bool Store::del(const std::string& key) {
  auto it = data.find(key);
  if (it == data.end()) return false;
  bool live = !expired(it->second, now_ms());
  if (!live) ++expired_keys;
  data.erase(it);
  return live;
}

void Store::flush() {
  data.clear();
  ++epoch;
  cv.notify_all();
}

size_t Store::size() const {
  int64_t now = now_ms();
  size_t total = 0;
  for (const auto& [_, entry] : data) {
    if (!expired(entry, now)) ++total;
  }
  return total;
}

size_t Store::expiring() const {
  int64_t now = now_ms();
  size_t total = 0;
  for (const auto& [_, entry] : data) {
    if (!expired(entry, now) && entry.expire_at != 0) ++total;
  }
  return total;
}

std::vector<std::string> sorted_keys(const Hash& hash) {
  std::vector<std::string> out;
  out.reserve(hash.size());
  for (const auto& [key, _] : hash) out.push_back(key);
  std::sort(out.begin(), out.end());
  return out;
}

std::vector<std::string> sorted_members(const Set& set) {
  std::vector<std::string> out(set.begin(), set.end());
  std::sort(out.begin(), out.end());
  return out;
}

}  // namespace redlite
