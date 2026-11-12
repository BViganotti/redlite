#include "server.hpp"

#include <arpa/inet.h>
#include <fcntl.h>
#include <netinet/in.h>
#include <signal.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <unistd.h>

#include <algorithm>
#include <cerrno>
#include <charconv>
#include <chrono>
#include <cmath>
#include <cstdio>
#include <cstring>
#include <iostream>
#include <sstream>

namespace redlite {

std::string upper_ascii(std::string value) {
  for (char& ch : value) ch = static_cast<char>(std::toupper(static_cast<unsigned char>(ch)));
  return value;
}

std::string lower_ascii(std::string value) {
  for (char& ch : value) ch = static_cast<char>(std::tolower(static_cast<unsigned char>(ch)));
  return value;
}

namespace {

std::string encode_command(const std::vector<std::string>& args) {
  std::string out;
  out.push_back('*');
  out += std::to_string(args.size());
  out += "\r\n";
  for (const auto& arg : args) {
    out.push_back('$');
    out += std::to_string(arg.size());
    out += "\r\n";
    out += arg;
    out += "\r\n";
  }
  return out;
}

bool send_all(int fd, const std::string& data) {
  size_t sent = 0;
  while (sent < data.size()) {
    ssize_t n = ::send(fd, data.data() + sent, data.size() - sent, 0);
    if (n < 0) {
      if (errno == EINTR) continue;
      return false;
    }
    if (n == 0) return false;
    sent += static_cast<size_t>(n);
  }
  return true;
}

std::string printable(const std::string& value) {
  std::string out;
  for (char ch : value) {
    unsigned char byte = static_cast<unsigned char>(ch);
    if (byte < 0x20 || byte > 0x7e) {
      out.push_back('?');
    } else {
      out.push_back(ch);
    }
  }
  if (out.size() > 128) out = out.substr(0, 128) + "...";
  return out;
}

std::string preview_args(const std::vector<std::string>& args, size_t start) {
  std::string out;
  for (size_t i = start; i < args.size() && i < start + 3; ++i) {
    if (!out.empty()) out.push_back(' ');
    out.push_back('\'');
    out += printable(args[i]);
    out.push_back('\'');
  }
  if (!out.empty()) out.push_back(' ');
  return out;
}

bool is_client_reply(const std::vector<std::string>& args) {
  return args.size() >= 2 && upper_ascii(args[0]) == "CLIENT" && upper_ascii(args[1]) == "REPLY";
}

std::string fmt_double(double value) { return format_double(value); }

}  // namespace

CommandMap& command_table() {
  static CommandMap table;
  return table;
}

bool arity_ok(int arity, size_t argc) {
  if (arity >= 0) return argc == static_cast<size_t>(arity);
  return argc >= static_cast<size_t>(-arity);
}

int64_t parse_int(const std::string& value) {
  if (value.empty()) throw RedisError("ERR value is not an integer or out of range");
  errno = 0;
  char* end = nullptr;
  long long parsed = std::strtoll(value.c_str(), &end, 10);
  if (errno != 0 || end != value.c_str() + value.size()) {
    throw RedisError("ERR value is not an integer or out of range");
  }
  return static_cast<int64_t>(parsed);
}

double parse_float(const std::string& value) {
  errno = 0;
  char* end = nullptr;
  double parsed = std::strtod(value.c_str(), &end);
  if (errno != 0 || end != value.c_str() + value.size() || std::isnan(parsed)) {
    throw RedisError("ERR value is not a valid float");
  }
  return parsed;
}

std::string format_score(double value) { return fmt_double(value); }

std::string format_geo_double(double value) {
  if (std::isinf(value)) return value > 0 ? "inf" : "-inf";
  char buf[64];
  auto result = std::to_chars(buf, buf + sizeof(buf), value, std::chars_format::general);
  if (result.ec == std::errc()) return std::string(buf, result.ptr);
  std::snprintf(buf, sizeof(buf), "%.17g", value);
  return buf;
}

// ---------------------------------------------------------------- Client

Client::Client(Server& server, int fd, uint64_t id, std::string addr)
    : srv(server), fd(fd), id(id), addr(std::move(addr)) {}

Client::~Client() {
  if (fd >= 0) ::close(fd);
}

void Client::send_raw(const std::string& bytes) {
  std::lock_guard<std::mutex> lock(write_mu_);
  if (fd < 0 || closing.load()) return;
  if (!send_all(fd, bytes)) closing.store(true);
}

void Client::send(const Reply& reply) {
  Writer writer(proto);
  send_raw(writer.encode(reply));
}

std::string Client::flags() const {
  if (subscribed()) return "P";
  return "N";
}

std::string Client::info_line() const {
  std::ostringstream out;
  out << "id=" << id << " addr=" << addr << " laddr=" << srv.config().bind << ":"
      << srv.port() << " fd=" << fd << " name=" << name << " age=0 idle=0 flags=" << flags()
      << " db=0 sub=" << subs.size() << " psub=" << psubs.size()
      << " ssub=" << ssubs.size() << " multi=-1 qbuf=0 qbuf-free=0 argv-mem=0 obl=0 oll=0 omem=0 tot-mem=0"
      << " events=r cmd=client|info user=default redir=-1 resp=" << proto;
  return out.str();
}

// ---------------------------------------------------------------- Server

Server::Server(Config config) : config_(std::move(config)) {
  started_ms = static_cast<uint64_t>(
      std::chrono::duration_cast<std::chrono::milliseconds>(
          std::chrono::system_clock::now().time_since_epoch())
          .count());
  auto& table = command_table();
  if (table.empty()) {
    register_core_commands(table);
    register_string_commands(table);
    register_hash_commands(table);
    register_list_commands(table);
    register_set_commands(table);
    register_zset_commands(table);
  }
  if (config_.appendonly) {
    std::string path = config_.dir + "/appendonly.aof";
    aof_fd_ = ::open(path.c_str(), O_CREAT | O_WRONLY | O_APPEND, 0644);
    if (aof_fd_ >= 0) load_aof();
  }
  expiry_thread_ = std::thread([this] { expiry_loop(); });
  fsync_thread_ = std::thread([this] { fsync_loop(); });
}

Server::~Server() {
  stop();
  if (aof_fd_ >= 0) {
    ::close(aof_fd_);
    aof_fd_ = -1;
  }
}

bool Server::listen() {
  listen_fd_ = ::socket(AF_INET, SOCK_STREAM, 0);
  if (listen_fd_ < 0) return false;
  int one = 1;
  ::setsockopt(listen_fd_, SOL_SOCKET, SO_REUSEADDR, &one, sizeof(one));
  sockaddr_in address{};
  address.sin_family = AF_INET;
  address.sin_port = htons(static_cast<uint16_t>(config_.port));
  if (::inet_pton(AF_INET, config_.bind.c_str(), &address.sin_addr) != 1) {
    ::close(listen_fd_);
    listen_fd_ = -1;
    return false;
  }
  if (::bind(listen_fd_, reinterpret_cast<sockaddr*>(&address), sizeof(address)) != 0) {
    ::close(listen_fd_);
    listen_fd_ = -1;
    return false;
  }
  if (::listen(listen_fd_, 511) != 0) {
    ::close(listen_fd_);
    listen_fd_ = -1;
    return false;
  }
  socklen_t len = sizeof(address);
  if (::getsockname(listen_fd_, reinterpret_cast<sockaddr*>(&address), &len) == 0) {
    actual_port_ = ntohs(address.sin_port);
  } else {
    actual_port_ = config_.port;
  }
  return true;
}

void Server::serve() { accept_loop(); }

void Server::stop() {
  bool expected = false;
  if (!shutting_down.compare_exchange_strong(expected, true)) return;
  if (listen_fd_ >= 0) {
    ::shutdown(listen_fd_, SHUT_RDWR);
    ::close(listen_fd_);
    listen_fd_ = -1;
  }
  {
    std::lock_guard<std::mutex> lock(clients_mu_);
    for (auto& [_, client] : clients_) {
      client->closing.store(true);
      ::shutdown(client->fd, SHUT_RDWR);
    }
  }
  store.cv.notify_all();
  if (expiry_thread_.joinable()) expiry_thread_.join();
  if (fsync_thread_.joinable()) fsync_thread_.join();
  {
    std::lock_guard<std::mutex> lock(threads_mu_);
    for (auto& thread : threads_) {
      if (thread.joinable()) thread.join();
    }
    threads_.clear();
  }
}

void Server::accept_loop() {
  while (!shutting_down.load()) {
    sockaddr_in peer{};
    socklen_t len = sizeof(peer);
    int fd = ::accept(listen_fd_, reinterpret_cast<sockaddr*>(&peer), &len);
    if (fd < 0) {
      if (errno == EINTR) continue;
      if (shutting_down.load()) break;
      break;
    }
    size_t client_count;
    {
      std::lock_guard<std::mutex> lock(clients_mu_);
      client_count = clients_.size();
    }
    if (static_cast<int>(client_count) >= config_.maxclients) {
      static const char message[] = "-ERR max number of clients reached\r\n";
      ::send(fd, message, sizeof(message) - 1, 0);
      ::close(fd);
      continue;
    }
    char host[INET_ADDRSTRLEN] = {0};
    ::inet_ntop(AF_INET, &peer.sin_addr, host, sizeof(host));
    std::string addr = std::string(host) + ":" + std::to_string(ntohs(peer.sin_port));
    uint64_t id;
    {
      std::lock_guard<std::mutex> lock(clients_mu_);
      id = next_client_id_++;
    }
    auto owned = std::make_unique<Client>(*this, fd, id, addr);
    Client* client = owned.get();
    {
      std::lock_guard<std::mutex> lock(clients_mu_);
      all_clients_.push_back(std::move(owned));
      clients_[id] = client;
    }
    total_connections.fetch_add(1);
    std::lock_guard<std::mutex> lock(threads_mu_);
    threads_.emplace_back([this, client] { serve_client(*client); });
  }
}

void Server::serve_client(Client& client) {
  Reader reader(client.fd);
  try {
    while (!shutting_down.load()) {
      std::vector<std::string> args;
      bool ok = reader.read_command(args);
      if (!ok) break;
      if (args.empty()) continue;
      total_commands.fetch_add(1);

      bool suppress = client.reply_off;
      if (client.reply_skip) {
        suppress = !is_client_reply(args);
        client.reply_skip = false;
      }

      Reply reply = execute(client, args);
      if (!suppress) client.send(reply);
      if (client.closing.load()) break;
    }
  } catch (const ProtocolError& error) {
    client.send(Reply::error(std::string("ERR Protocol error: ") + error.message));
  } catch (const Reader::IOError&) {
    // peer disconnected or socket closed
  } catch (const std::exception& error) {
    client.send(Reply::error(std::string("ERR ") + error.what()));
  }

  remove_subscriptions(client);
  client.closing.store(true);
  {
    std::lock_guard<std::mutex> lock(clients_mu_);
    clients_.erase(client.id);
  }
  if (client.fd >= 0) {
    ::close(client.fd);
    client.fd = -1;
  }
}

Reply Server::execute(Client& client, std::vector<std::string>& args) {
  std::string name = upper_ascii(args[0]);

  if (client.proto < 3 && client.subscribed() &&
      !(name == "SUBSCRIBE" || name == "UNSUBSCRIBE" || name == "PSUBSCRIBE" ||
        name == "PUNSUBSCRIBE" || name == "SSUBSCRIBE" || name == "SUNSUBSCRIBE" ||
        name == "SPUBLISH" || name == "PING" || name == "QUIT" || name == "RESET")) {
    return Reply::error("ERR Can't execute '" + lower_ascii(name) +
                        "': only (P|S)SUBSCRIBE / (P|S)UNSUBSCRIBE / PING / QUIT / RESET are allowed in this context");
  }

  if (client.multi && name != "EXEC" && name != "DISCARD" && name != "MULTI" &&
      name != "WATCH" && name != "QUIT" && name != "RESET") {
    auto it = command_table().find(name);
    if (it == command_table().end()) {
      client.dirty = true;
      return Reply::error("ERR unknown command '" + printable(args[0]) +
                          "', with args beginning with: " + preview_args(args, 1));
    }
    if (!arity_ok(it->second.arity, args.size())) {
      client.dirty = true;
      return Reply::error("ERR wrong number of arguments for '" + lower_ascii(name) + "' command");
    }
    client.queued.push_back(args);
    return Reply::simple("QUEUED");
  }

  try {
    CommandResult result = execute_locked(client, args, true);
    return result.reply;
  } catch (const RedisError& error) {
    return Reply::error(error.what());
  } catch (const std::exception& error) {
    return Reply::error(std::string("ERR ") + error.what());
  }
}

CommandResult Server::execute_locked(Client& client, std::vector<std::string>& args,
                                     bool propagate_commands) {
  std::string name = upper_ascii(args[0]);
  auto it = command_table().find(name);
  if (it == command_table().end()) {
    throw RedisError("ERR unknown command '" + printable(args[0]) +
                     "', with args beginning with: " + preview_args(args, 1));
  }
  const Command& command = it->second;
  if (!arity_ok(command.arity, args.size())) {
    throw RedisError("ERR wrong number of arguments for '" + lower_ascii(name) + "' command");
  }

  CommandResult result = command.handler(client, args);
  if (result.mutated && propagate_commands && !replaying.load()) {
    const std::vector<std::string>& payload = result.rewrite.empty() ? args : result.rewrite;
    aof_append(payload);
    propagate(payload);
  }
  return result;
}

void Server::propagate(const std::vector<std::string>& args) {
  // Replication is not implemented in the C++ port yet. The hook exists so the
  // command layer already has canonical rewrite semantics.
  (void)args;
}

void Server::aof_append(const std::vector<std::string>& args) {
  if (aof_fd_ < 0) return;
  std::string encoded = encode_command(args);
  {
    std::lock_guard<std::mutex> lock(aof_mu_);
    size_t sent = 0;
    while (sent < encoded.size()) {
      ssize_t n = ::write(aof_fd_, encoded.data() + sent, encoded.size() - sent);
      if (n < 0) {
        if (errno == EINTR) continue;
        return;
      }
      sent += static_cast<size_t>(n);
    }
    if (config_.appendfsync == "always") {
      ::fsync(aof_fd_);
    } else if (config_.appendfsync == "everysec") {
      fsync_dirty_.store(true);
    }
  }
}

void Server::load_aof() {
  std::string path = config_.dir + "/appendonly.aof";
  int fd = ::open(path.c_str(), O_RDONLY);
  if (fd < 0) return;
  Reader reader(fd);
  Client replay(*this, -1, 0, "replay");
  replay.proto = 2;
  replaying.store(true);
  for (;;) {
    std::vector<std::string> args;
    try {
      if (!reader.read_command(args)) break;
    } catch (const Reader::IOError&) {
      break;
    } catch (const ProtocolError& error) {
      (void)error;
      break;
    }
    if (args.empty()) continue;
    try {
      execute_locked(replay, args, false);
    } catch (const std::exception& error) {
      (void)error;
    }
  }
  replaying.store(false);
  ::close(fd);
}

void Server::expiry_loop() {
  while (!shutting_down.load()) {
    std::this_thread::sleep_for(std::chrono::milliseconds(100));
    std::lock_guard<std::recursive_mutex> lock(store.mu);
    int64_t now = store.now_ms();
    bool removed = false;
    for (auto it = store.data.begin(); it != store.data.end();) {
      if (Store::expired(it->second, now)) {
        it = store.data.erase(it);
        ++store.expired_keys;
        removed = true;
      } else {
        ++it;
      }
    }
    if (removed) store.cv.notify_all();
  }
}

void Server::fsync_loop() {
  while (!shutting_down.load()) {
    std::this_thread::sleep_for(std::chrono::seconds(1));
    if (aof_fd_ >= 0 && fsync_dirty_.exchange(false)) {
      std::lock_guard<std::mutex> lock(aof_mu_);
      ::fsync(aof_fd_);
    }
  }
}

int Server::publish(const std::string& channel, const std::string& payload) {
  struct Target {
    Client* client;
    std::string pattern;
  };
  std::vector<Client*> direct;
  std::vector<Target> matched;
  {
    std::lock_guard<std::mutex> lock(pubsub_mu_);
    auto it = channels_.find(channel);
    if (it != channels_.end()) {
      for (Client* client : it->second) direct.push_back(client);
    }
    // Pattern matching is done by the caller's glob implementation; for the
    // C++ core we use a small glob matcher in commands_core.cpp via callback.
    extern bool glob_match_local(const std::string&, const std::string&);
    for (const auto& [pattern, clients] : patterns_) {
      if (!glob_match_local(pattern, channel)) continue;
      for (Client* client : clients) matched.push_back({client, pattern});
    }
  }
  int deliveries = 0;
  for (Client* client : direct) {
    client->send(Reply::push({Reply::bulk("message"), Reply::bulk(channel), Reply::bulk(payload)}));
    ++deliveries;
  }
  for (const auto& target : matched) {
    target.client->send(Reply::push({Reply::bulk("pmessage"), Reply::bulk(target.pattern),
                                     Reply::bulk(channel), Reply::bulk(payload)}));
    ++deliveries;
  }
  return deliveries;
}

int Server::publish_shard(const std::string& channel, const std::string& payload) {
  std::vector<Client*> direct;
  {
    std::lock_guard<std::mutex> lock(pubsub_mu_);
    auto it = shards_.find(channel);
    if (it != shards_.end()) {
      for (Client* client : it->second) direct.push_back(client);
    }
  }
  for (Client* client : direct) {
    client->send(Reply::push({Reply::bulk("smessage"), Reply::bulk(channel), Reply::bulk(payload)}));
  }
  return static_cast<int>(direct.size());
}

void Server::subscribe(Client& client, const std::string& channel) {
  std::lock_guard<std::mutex> lock(pubsub_mu_);
  channels_[channel].insert(&client);
  client.subs.insert(channel);
}
void Server::unsubscribe(Client& client, const std::string& channel) {
  std::lock_guard<std::mutex> lock(pubsub_mu_);
  auto it = channels_.find(channel);
  if (it != channels_.end()) {
    it->second.erase(&client);
    if (it->second.empty()) channels_.erase(it);
  }
  client.subs.erase(channel);
}
void Server::psubscribe(Client& client, const std::string& pattern) {
  std::lock_guard<std::mutex> lock(pubsub_mu_);
  patterns_[pattern].insert(&client);
  client.psubs.insert(pattern);
}
void Server::punsubscribe(Client& client, const std::string& pattern) {
  std::lock_guard<std::mutex> lock(pubsub_mu_);
  auto it = patterns_.find(pattern);
  if (it != patterns_.end()) {
    it->second.erase(&client);
    if (it->second.empty()) patterns_.erase(it);
  }
  client.psubs.erase(pattern);
}
void Server::ssubscribe(Client& client, const std::string& channel) {
  std::lock_guard<std::mutex> lock(pubsub_mu_);
  shards_[channel].insert(&client);
  client.ssubs.insert(channel);
}
void Server::sunsubscribe(Client& client, const std::string& channel) {
  std::lock_guard<std::mutex> lock(pubsub_mu_);
  auto it = shards_.find(channel);
  if (it != shards_.end()) {
    it->second.erase(&client);
    if (it->second.empty()) shards_.erase(it);
  }
  client.ssubs.erase(channel);
}

void Server::remove_subscriptions(Client& client) {
  std::lock_guard<std::mutex> lock(pubsub_mu_);
  for (const auto& channel : client.subs) {
    auto it = channels_.find(channel);
    if (it != channels_.end()) {
      it->second.erase(&client);
      if (it->second.empty()) channels_.erase(it);
    }
  }
  for (const auto& pattern : client.psubs) {
    auto it = patterns_.find(pattern);
    if (it != patterns_.end()) {
      it->second.erase(&client);
      if (it->second.empty()) patterns_.erase(it);
    }
  }
  for (const auto& channel : client.ssubs) {
    auto it = shards_.find(channel);
    if (it != shards_.end()) {
      it->second.erase(&client);
      if (it->second.empty()) shards_.erase(it);
    }
  }
}

std::vector<std::string> Server::channel_names(const std::string& pattern) const {
  extern bool glob_match_local(const std::string&, const std::string&);
  std::vector<std::string> out;
  std::lock_guard<std::mutex> lock(pubsub_mu_);
  for (const auto& [channel, _] : channels_) {
    if (pattern.empty() || glob_match_local(pattern, channel)) out.push_back(channel);
  }
  std::sort(out.begin(), out.end());
  return out;
}

std::vector<std::string> Server::shard_channel_names(const std::string& pattern) const {
  extern bool glob_match_local(const std::string&, const std::string&);
  std::vector<std::string> out;
  std::lock_guard<std::mutex> lock(pubsub_mu_);
  for (const auto& [channel, _] : shards_) {
    if (pattern.empty() || glob_match_local(pattern, channel)) out.push_back(channel);
  }
  std::sort(out.begin(), out.end());
  return out;
}

int Server::subscriber_count(const std::string& channel) const {
  std::lock_guard<std::mutex> lock(pubsub_mu_);
  auto it = channels_.find(channel);
  return it == channels_.end() ? 0 : static_cast<int>(it->second.size());
}
int Server::shard_subscriber_count(const std::string& channel) const {
  std::lock_guard<std::mutex> lock(pubsub_mu_);
  auto it = shards_.find(channel);
  return it == shards_.end() ? 0 : static_cast<int>(it->second.size());
}
int Server::pattern_count() const {
  std::lock_guard<std::mutex> lock(pubsub_mu_);
  return static_cast<int>(patterns_.size());
}

std::map<std::string, std::string> Server::config_snapshot() const {
  return {
      {"bind", config_.bind},
      {"port", std::to_string(port())},
      {"dir", config_.dir},
      {"dbfilename", config_.dbfilename},
      {"maxclients", std::to_string(config_.maxclients)},
      {"appendonly", config_.appendonly ? "yes" : "no"},
      {"appendfsync", config_.appendfsync},
      {"loglevel", config_.loglevel},
      {"databases", "1"},
      {"maxmemory", "0"},
      {"maxmemory-policy", "noeviction"},
      {"slowlog-log-slower-than", "10000"},
      {"slowlog-max-len", "128"},
      {"requirepass", ""},
      {"notify-keyspace-events", ""},
  };
}

void Server::set_config(const std::string& key, const std::string& value) {
  std::string name = lower_ascii(key);
  if (name == "maxclients") {
    int64_t n = parse_int(value);
    if (n < 1) throw RedisError("ERR Invalid argument '" + value + "' for CONFIG SET 'maxclients'");
    config_.maxclients = static_cast<int>(n);
    return;
  }
  if (name == "appendfsync") {
    if (value != "always" && value != "everysec" && value != "no") {
      throw RedisError("ERR Invalid argument '" + value + "' for CONFIG SET 'appendfsync'");
    }
    config_.appendfsync = value;
    return;
  }
  if (name == "loglevel") {
    std::string level = lower_ascii(value);
    if (level != "debug" && level != "info" && level != "warn" && level != "error") {
      throw RedisError("ERR Invalid argument '" + value + "' for CONFIG SET 'loglevel'");
    }
    config_.loglevel = level;
    return;
  }
  throw RedisError("ERR Unsupported CONFIG parameter: " + key);
}

void Server::bgrewrite_aof() {
  if (aof_fd_ < 0) return;
  std::string temp = config_.dir + "/appendonly.aof.tmp";
  int fd = ::open(temp.c_str(), O_CREAT | O_TRUNC | O_WRONLY, 0644);
  if (fd < 0) throw RedisError("ERR cannot open temporary AOF file");
  auto write_all = [&](const std::string& data) {
    size_t sent = 0;
    while (sent < data.size()) {
      ssize_t n = ::write(fd, data.data() + sent, data.size() - sent);
      if (n < 0) {
        if (errno == EINTR) continue;
        break;
      }
      sent += static_cast<size_t>(n);
    }
  };
  {
    std::lock_guard<std::recursive_mutex> lock(store.mu);
    for (const auto& [key, entry] : store.data) {
      if (Store::expired(entry, store.now_ms())) continue;
      std::vector<std::string> command;
      switch (value_type(entry.value)) {
        case ValueType::String: {
          const auto& value = std::get<String>(entry.value);
          command = {"SET", key, value};
          break;
        }
        case ValueType::Hash: {
          const auto& hash = std::get<Hash>(entry.value);
          command = {"HSET", key};
          for (const auto& field : sorted_keys(hash)) {
            command.push_back(field);
            command.push_back(hash.at(field));
          }
          break;
        }
        case ValueType::List: {
          const auto& list = std::get<List>(entry.value);
          command = {"RPUSH", key};
          for (const auto& item : list) command.push_back(item);
          break;
        }
        case ValueType::Set: {
          const auto& set = std::get<Set>(entry.value);
          command = {"SADD", key};
          for (const auto& member : sorted_members(set)) command.push_back(member);
          break;
        }
        case ValueType::ZSet: {
          const auto& zset = std::get<ZSet>(entry.value);
          command = {"ZADD", key};
          for (const auto& [member, score] : zset.ordered()) {
            command.push_back(format_score(score));
            command.push_back(member);
          }
          break;
        }
      }
      if (command.size() > 2) write_all(encode_command(command));
      if (entry.expire_at != 0) {
        write_all(encode_command({"PEXPIREAT", key, std::to_string(entry.expire_at)}));
      }
    }
  }
  ::fsync(fd);
  ::close(fd);
  std::string path = config_.dir + "/appendonly.aof";
  ::rename(temp.c_str(), path.c_str());
  {
    std::lock_guard<std::mutex> lock(aof_mu_);
    if (aof_fd_ >= 0) ::close(aof_fd_);
    aof_fd_ = ::open(path.c_str(), O_CREAT | O_WRONLY | O_APPEND, 0644);
  }
}

std::string Server::client_list() const {
  std::lock_guard<std::mutex> lock(clients_mu_);
  std::string out;
  for (const auto& [_, client] : clients_) {
    out += client->info_line();
    out.push_back('\n');
  }
  return out;
}

std::string Server::info(const std::vector<std::string>& sections) const {
  (void)sections;
  uint64_t uptime = (static_cast<uint64_t>(std::chrono::duration_cast<std::chrono::milliseconds>(
                          std::chrono::system_clock::now().time_since_epoch())
                          .count()) -
                     started_ms) /
                    1000;
  std::ostringstream out;
  out << "# Server\r\n";
  out << "redis_version:8.8.0\r\n";
  out << "redlite_impl:cpp\r\n";
  out << "process_id:" << ::getpid() << "\r\n";
  out << "tcp_port:" << port() << "\r\n";
  out << "uptime_in_seconds:" << uptime << "\r\n";
  out << "# Clients\r\n";
  {
    std::lock_guard<std::mutex> lock(clients_mu_);
    out << "connected_clients:" << clients_.size() << "\r\n";
  }
  out << "maxclients:" << config_.maxclients << "\r\n";
  out << "# Memory\r\n";
  out << "used_memory:" << store.size() * 64 << "\r\n";
  out << "# Persistence\r\n";
  out << "aof_enabled:" << (config_.appendonly ? 1 : 0) << "\r\n";
  out << "# Stats\r\n";
  out << "total_connections_received:" << total_connections.load() << "\r\n";
  out << "total_commands_processed:" << total_commands.load() << "\r\n";
  out << "expired_keys:" << store.expired_keys << "\r\n";
  out << "# Replication\r\nrole:master\r\nconnected_slaves:0\r\n";
  out << "# Keyspace\r\n";
  size_t keys = store.size();
  if (keys > 0) {
    out << "db0:keys=" << keys << ",expires=" << store.expiring() << ",avg_ttl=0\r\n";
  }
  return out.str();
}

}  // namespace redlite
