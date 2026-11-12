#pragma once

#include <atomic>
#include <cstdint>
#include <functional>
#include <map>
#include <memory>
#include <mutex>
#include <set>
#include <string>
#include <thread>
#include <unordered_map>
#include <vector>

#include "resp.hpp"
#include "store.hpp"

namespace redlite {

enum CommandFlag : uint32_t {
  kWrite = 1 << 0,
  kReadOnly = 1 << 1,
  kAdmin = 1 << 2,
  kFast = 1 << 3,
  kBlocking = 1 << 4,
};

struct CommandResult {
  Reply reply;
  bool mutated = false;
  std::vector<std::string> rewrite;
};

class Server;
class Client;

struct Command {
  std::string name;
  int arity = 0;
  uint32_t flags = 0;
  std::function<CommandResult(Client&, std::vector<std::string>&)> handler;
};

using CommandMap = std::unordered_map<std::string, Command>;
CommandMap& command_table();
bool arity_ok(int arity, size_t argc);

class Client {
 public:
  Client(Server& server, int fd, uint64_t id, std::string addr);
  ~Client();

  Server& srv;
  int fd;
  uint64_t id;
  std::string addr;
  int proto = 2;
  bool authed = true;
  std::string name;
  bool multi = false;
  bool executing = false;
  bool dirty = false;
  std::vector<std::vector<std::string>> queued;
  std::unordered_map<std::string, WatchState> watched;
  std::set<std::string> subs;
  std::set<std::string> psubs;
  std::set<std::string> ssubs;
  std::atomic<bool> closing{false};

  // CLIENT REPLY state.
  bool reply_off = false;
  bool reply_skip = false;

  void send(const Reply& reply);
  void send_raw(const std::string& bytes);
  bool subscribed() const { return !subs.empty() || !psubs.empty() || !ssubs.empty(); }

  std::string flags() const;
  std::string info_line() const;

 private:
  std::mutex write_mu_;
};

class Server {
 public:
  struct Config {
    std::string bind = "127.0.0.1";
    int port = 6379;
    std::string dir = ".";
    bool appendonly = false;
    std::string appendfsync = "everysec";
    std::string dbfilename = "dump.rdb";
    int maxclients = 10000;
    std::string loglevel = "info";
  };

  explicit Server(Config config);
  ~Server();
  Server(const Server&) = delete;
  Server& operator=(const Server&) = delete;

  bool listen();
  void serve();
  void stop();
  int port() const { return actual_port_; }
  const Config& config() const { return config_; }

  Reply execute(Client& client, std::vector<std::string>& args);
  CommandResult execute_locked(Client& client, std::vector<std::string>& args, bool propagate);
  void propagate(const std::vector<std::string>& args);
  void aof_append(const std::vector<std::string>& args);

  // Pub/Sub. Returns the number of deliveries.
  int publish(const std::string& channel, const std::string& payload);
  int publish_shard(const std::string& channel, const std::string& payload);
  void subscribe(Client& client, const std::string& channel);
  void unsubscribe(Client& client, const std::string& channel);
  void psubscribe(Client& client, const std::string& pattern);
  void punsubscribe(Client& client, const std::string& pattern);
  void ssubscribe(Client& client, const std::string& channel);
  void sunsubscribe(Client& client, const std::string& channel);
  void remove_subscriptions(Client& client);
  std::vector<std::string> channel_names(const std::string& pattern) const;
  std::vector<std::string> shard_channel_names(const std::string& pattern) const;
  int subscriber_count(const std::string& channel) const;
  int shard_subscriber_count(const std::string& channel) const;
  int pattern_count() const;

  std::string info(const std::vector<std::string>& sections) const;
  std::string client_list() const;

  Store store;
  std::atomic<bool> shutting_down{false};
  std::atomic<bool> replaying{false};
  uint64_t started_ms = 0;
  std::atomic<uint64_t> total_commands{0};
  std::atomic<uint64_t> total_connections{0};

  std::map<std::string, std::string> config_snapshot() const;
  void set_config(const std::string& key, const std::string& value);

  // Snapshot / rewrite support.
  void bgrewrite_aof();

 private:
  void accept_loop();
  void serve_client(Client& client);
  void expiry_loop();
  void fsync_loop();
  void load_aof();
  std::vector<std::string> command_keys(const std::string& name,
                                        const std::vector<std::string>& args) const;

  Config config_;
  int listen_fd_ = -1;
  int actual_port_ = 0;
  std::thread expiry_thread_;
  std::thread fsync_thread_;
  std::mutex threads_mu_;
  std::vector<std::thread> threads_;
  std::atomic<bool> fsync_dirty_{false};
  int aof_fd_ = -1;
  mutable std::mutex aof_mu_;
  mutable std::mutex clients_mu_;
  std::map<uint64_t, Client*> clients_;
  std::vector<std::unique_ptr<Client>> all_clients_;
  mutable std::mutex pubsub_mu_;
  std::unordered_map<std::string, std::set<Client*>> channels_;
  std::unordered_map<std::string, std::set<Client*>> shards_;
  std::unordered_map<std::string, std::set<Client*>> patterns_;
  uint64_t next_client_id_ = 1;
};

// Registration functions implemented in commands_*.cpp.
void register_core_commands(CommandMap& table);
void register_string_commands(CommandMap& table);
void register_hash_commands(CommandMap& table);
void register_list_commands(CommandMap& table);
void register_set_commands(CommandMap& table);
void register_zset_commands(CommandMap& table);

// Shared parse helpers.
int64_t parse_int(const std::string& value);
double parse_float(const std::string& value);
std::string format_score(double value);
std::string format_geo_double(double value);
std::string upper_ascii(std::string value);
std::string lower_ascii(std::string value);
bool glob_match_local(const std::string& pattern, const std::string& value);

}  // namespace redlite
