#include <csignal>
#include <sys/stat.h>
#include <cstdlib>
#include <cstring>
#include <iostream>
#include <string>

#include "server.hpp"

namespace {
redlite::Server* g_server = nullptr;

void handle_signal(int) {
  if (g_server != nullptr) g_server->stop();
}
}  // namespace

int main(int argc, char** argv) {
  redlite::Server::Config config;
  for (int i = 1; i < argc; ++i) {
    std::string arg = argv[i];
    auto next = [&](const char* name) -> std::string {
      if (i + 1 >= argc) {
        std::cerr << "missing value for " << name << "\n";
        std::exit(1);
      }
      return argv[++i];
    };
    if (arg == "--port" || arg == "-p") {
      config.port = std::stoi(next("--port"));
    } else if (arg == "--bind") {
      config.bind = next("--bind");
    } else if (arg == "--dir") {
      config.dir = next("--dir");
    } else if (arg == "--appendonly") {
      std::string value = next("--appendonly");
      config.appendonly = value == "yes" || value == "1" || value == "true";
    } else if (arg == "--appendfsync") {
      config.appendfsync = next("--appendfsync");
    } else if (arg == "--loglevel") {
      config.loglevel = next("--loglevel");
    } else if (arg == "--dbfilename") {
      config.dbfilename = next("--dbfilename");
    } else if (arg == "--maxclients") {
      config.maxclients = std::stoi(next("--maxclients"));
    } else if (arg == "--version" || arg == "-v") {
      std::cout << "redlite cpp 0.1.0\n";
      return 0;
    } else if (arg == "--help" || arg == "-h") {
      std::cout << "Usage: redlite [--port 6379] [--bind 127.0.0.1] [--dir .] "
                   "[--appendonly yes|no]\n";
      return 0;
    } else {
      std::cerr << "unknown argument: " << arg << "\n";
      return 1;
    }
  }

  ::mkdir(config.dir.c_str(), 0755);
  redlite::Server server(config);
  g_server = &server;
  std::signal(SIGINT, handle_signal);
  std::signal(SIGTERM, handle_signal);
  std::signal(SIGPIPE, SIG_IGN);

  if (!server.listen()) {
    std::cerr << "failed to bind " << config.bind << ":" << config.port << "\n";
    return 1;
  }
  std::cout << "redlite cpp listening on " << config.bind << ":" << server.port() << "\n";
  server.serve();
  server.stop();
  return 0;
}
