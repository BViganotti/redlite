#include <sys/socket.h>
#include <unistd.h>

#include <cassert>
#include <iostream>
#include <string>
#include <vector>

#include "resp.hpp"
#include "server.hpp"

namespace {
int failures = 0;

void check(bool condition, const std::string& name) {
  if (!condition) {
    std::cerr << "FAIL: " << name << "\n";
    ++failures;
  }
}
}  // namespace

int main() {
  using namespace redlite;

  // RESP writer, RESP2 and RESP3.
  {
    Writer writer(2);
    std::string out = writer.encode(Reply::array({Reply::bulk("x"), Reply::integer_reply(2),
                                                  Reply::null(), Reply::simple("OK")}));
    check(out == "*4\r\n$1\r\nx\r\n:2\r\n$-1\r\n+OK\r\n", "resp2 array");
    writer.set_proto(3);
    out = writer.encode(Reply::map({{Reply::bulk("a"), Reply::integer_reply(1)}}));
    check(out == "%1\r\n$1\r\na\r\n:1\r\n", "resp3 map");
    out = writer.encode(Reply::set({Reply::bulk("a")}));
    check(out == "~1\r\n$1\r\na\r\n", "resp3 set");
    out = writer.encode(Reply::double_reply(1.5));
    check(out == ",1.5\r\n", "resp3 double");
  }

  // RESP reader over a socket pair.
  {
    int fds[2];
    check(::socketpair(AF_UNIX, SOCK_STREAM, 0, fds) == 0, "socketpair");
    std::string request = "*3\r\n$3\r\nSET\r\n$1\r\nk\r\n$1\r\nv\r\n";
    check(::write(fds[1], request.data(), request.size()) == static_cast<ssize_t>(request.size()),
          "write request");
    Reader reader(fds[0]);
    std::vector<std::string> args;
    check(reader.read_command(args), "read command");
    check(args.size() == 3 && args[0] == "SET" && args[1] == "k" && args[2] == "v",
          "parsed command");
    ::close(fds[0]);
    ::close(fds[1]);
  }

  // Glob matcher used by KEYS/SCAN/PUBSUB.
  {
    check(glob_match_local("foo*", "foobar"), "glob star");
    check(!glob_match_local("foo*", "barfoo"), "glob star mismatch");
    check(glob_match_local("f?o", "foo"), "glob question");
    check(glob_match_local("[a-c]oo", "boo"), "glob class");
    check(!glob_match_local("[^a-c]oo", "boo"), "glob negated class");
  }

  // ZSet ordering and score replacement.
  {
    ZSet zset;
    zset.insert("b", 2);
    zset.insert("a", 1);
    zset.insert("c", 3);
    auto ordered = zset.ordered();
    check(ordered.size() == 3 && ordered[0].first == "a" && ordered[2].first == "c",
          "zset order");
    zset.insert("b", 0.5);
    ordered = zset.ordered();
    check(ordered[0].first == "b", "zset score replacement");
  }

  if (failures == 0) {
    std::cout << "all tests passed\n";
    return 0;
  }
  std::cerr << failures << " test(s) failed\n";
  return 1;
}
