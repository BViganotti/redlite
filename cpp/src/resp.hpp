#pragma once

#include <cstdint>
#include <string>
#include <utility>
#include <vector>

namespace redlite {

// One RESP reply. The same tree is encoded as RESP2 or RESP3 depending on the
// writer's protocol version, so command handlers never branch on protocol.
struct Reply {
  enum class Kind {
    Simple,
    Error,
    Integer,
    Bulk,
    Null,
    NullArray,
    Array,
    Map,
    Set,
    Push,
    Double,
    Bool,
    BigNumber,
    Verbatim,
    Pairs,
    Multi,
  };

  Kind kind = Kind::Null;
  std::string text;
  int64_t integer = 0;
  double number = 0;
  bool bool_value = false;
  std::vector<Reply> items;
  std::vector<std::pair<Reply, Reply>> pairs;
  std::string format; // verbatim three-character format

  static Reply simple(std::string value);
  static Reply error(std::string value);
  static Reply integer_reply(int64_t value);
  static Reply bulk(std::string value);
  static Reply null();
  static Reply null_array();
  static Reply array(std::vector<Reply> values = {});
  static Reply map(std::vector<std::pair<Reply, Reply>> values = {});
  static Reply set(std::vector<Reply> values = {});
  static Reply push(std::vector<Reply> values = {});
  static Reply double_reply(double value);
  static Reply boolean(bool value);
  static Reply big_number(std::string value);
  static Reply verbatim(std::string format, std::string value);
  static Reply multi(std::vector<Reply> values = {});
  static Reply pair_array(std::vector<Reply> flat);
};

std::string format_double(double value);

class Writer {
 public:
  explicit Writer(int proto = 2) : proto_(proto) {}
  void set_proto(int proto) { proto_ = proto; }
  int proto() const { return proto_; }
  std::string encode(const Reply& reply) const;

 private:
  void append(std::string& out, const Reply& reply) const;
  int proto_;
};

struct ProtocolError {
  std::string message;
};

class Reader {
 public:
  explicit Reader(int fd);
  // Reads one command. Returns false on a clean EOF; throws ProtocolError or
  // IOError on malformed input / socket failure.
  bool read_command(std::vector<std::string>& args);

  struct IOError {};

 private:
  std::string read_line();
  std::string read_exact(size_t size);
  int fd_;
  std::string buffer_;
  size_t pos_ = 0;
  bool eof_ = false;
};

// Resets a connection to RESP2/3 negotiation defaults.
inline int normalize_proto(int value) { return value == 3 ? 3 : 2; }

}  // namespace redlite
