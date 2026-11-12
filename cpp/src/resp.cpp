#include "resp.hpp"

#include <arpa/inet.h>
#include <sys/socket.h>
#include <unistd.h>

#include <algorithm>
#include <cerrno>
#include <charconv>
#include <cstdio>
#include <cstring>
#include <limits>

namespace redlite {

namespace {
constexpr int64_t kMaxBulk = 512LL * 1024 * 1024;
constexpr size_t kMaxArray = 1024 * 1024;
constexpr size_t kMaxInline = 64 * 1024;

std::string sanitize(std::string value) {
  for (char& ch : value) {
    if (ch == '\r' || ch == '\n') ch = ' ';
  }
  return value;
}

void append_header(std::string& out, char prefix, int64_t n) {
  out.push_back(prefix);
  out += std::to_string(n);
  out += "\r\n";
}
}  // namespace

Reply Reply::simple(std::string value) {
  Reply r;
  r.kind = Kind::Simple;
  r.text = std::move(value);
  return r;
}
Reply Reply::error(std::string value) {
  Reply r;
  r.kind = Kind::Error;
  r.text = std::move(value);
  return r;
}
Reply Reply::integer_reply(int64_t value) {
  Reply r;
  r.kind = Kind::Integer;
  r.integer = value;
  return r;
}
Reply Reply::bulk(std::string value) {
  Reply r;
  r.kind = Kind::Bulk;
  r.text = std::move(value);
  return r;
}
Reply Reply::null() {
  Reply r;
  r.kind = Kind::Null;
  return r;
}
Reply Reply::null_array() {
  Reply r;
  r.kind = Kind::NullArray;
  return r;
}
Reply Reply::array(std::vector<Reply> values) {
  Reply r;
  r.kind = Kind::Array;
  r.items = std::move(values);
  return r;
}
Reply Reply::map(std::vector<std::pair<Reply, Reply>> values) {
  Reply r;
  r.kind = Kind::Map;
  r.pairs = std::move(values);
  return r;
}
Reply Reply::set(std::vector<Reply> values) {
  Reply r;
  r.kind = Kind::Set;
  r.items = std::move(values);
  return r;
}
Reply Reply::push(std::vector<Reply> values) {
  Reply r;
  r.kind = Kind::Push;
  r.items = std::move(values);
  return r;
}
Reply Reply::double_reply(double value) {
  Reply r;
  r.kind = Kind::Double;
  r.number = value;
  return r;
}
Reply Reply::boolean(bool value) {
  Reply r;
  r.kind = Kind::Bool;
  r.bool_value = value;
  return r;
}
Reply Reply::big_number(std::string value) {
  Reply r;
  r.kind = Kind::BigNumber;
  r.text = std::move(value);
  return r;
}
Reply Reply::verbatim(std::string format, std::string value) {
  Reply r;
  r.kind = Kind::Verbatim;
  r.format = std::move(format);
  r.text = std::move(value);
  return r;
}
Reply Reply::multi(std::vector<Reply> values) {
  Reply r;
  r.kind = Kind::Multi;
  r.items = std::move(values);
  return r;
}
Reply Reply::pair_array(std::vector<Reply> flat) {
  Reply r;
  r.kind = Kind::Pairs;
  r.items = std::move(flat);
  return r;
}

std::string format_double(double value) {
  if (std::isinf(value)) return value > 0 ? "inf" : "-inf";
  if (std::isnan(value)) return "nan";
  if (value == 0) return "0";
  char buf[64];
  auto result = std::to_chars(buf, buf + sizeof(buf), value, std::chars_format::general);
  if (result.ec == std::errc()) return std::string(buf, result.ptr);
  std::snprintf(buf, sizeof(buf), "%.17g", value);
  return buf;
}

std::string Writer::encode(const Reply& reply) const {
  std::string out;
  append(out, reply);
  return out;
}

void Writer::append(std::string& out, const Reply& reply) const {
  switch (reply.kind) {
    case Reply::Kind::Simple:
      out.push_back('+');
      out += sanitize(reply.text);
      out += "\r\n";
      break;
    case Reply::Kind::Error:
      out.push_back('-');
      out += sanitize(reply.text);
      out += "\r\n";
      break;
    case Reply::Kind::Integer:
      append_header(out, ':', reply.integer);
      break;
    case Reply::Kind::Bulk:
      append_header(out, '$', static_cast<int64_t>(reply.text.size()));
      out += reply.text;
      out += "\r\n";
      break;
    case Reply::Kind::Null:
      out += proto_ >= 3 ? "_\r\n" : "$-1\r\n";
      break;
    case Reply::Kind::NullArray:
      out += proto_ >= 3 ? "_\r\n" : "*-1\r\n";
      break;
    case Reply::Kind::Array:
      append_header(out, '*', static_cast<int64_t>(reply.items.size()));
      for (const auto& item : reply.items) append(out, item);
      break;
    case Reply::Kind::Set:
      append_header(out, proto_ >= 3 ? '~' : '*', static_cast<int64_t>(reply.items.size()));
      for (const auto& item : reply.items) append(out, item);
      break;
    case Reply::Kind::Push:
      append_header(out, proto_ >= 3 ? '>' : '*', static_cast<int64_t>(reply.items.size()));
      for (const auto& item : reply.items) append(out, item);
      break;
    case Reply::Kind::Map:
      append_header(out, proto_ >= 3 ? '%' : '*',
                    static_cast<int64_t>(proto_ >= 3 ? reply.pairs.size() : reply.pairs.size() * 2));
      for (const auto& pair : reply.pairs) {
        append(out, pair.first);
        append(out, pair.second);
      }
      break;
    case Reply::Kind::Double:
      if (proto_ >= 3) {
        out.push_back(',');
        out += format_double(reply.number);
        out += "\r\n";
      } else {
        std::string text = format_double(reply.number);
        append_header(out, '$', static_cast<int64_t>(text.size()));
        out += text;
        out += "\r\n";
      }
      break;
    case Reply::Kind::Bool:
      if (proto_ >= 3) {
        out += reply.bool_value ? "#t\r\n" : "#f\r\n";
      } else {
        out += reply.bool_value ? ":1\r\n" : ":0\r\n";
      }
      break;
    case Reply::Kind::BigNumber:
      if (proto_ >= 3) {
        out.push_back('(');
        out += reply.text;
        out += "\r\n";
      } else {
        append_header(out, '$', static_cast<int64_t>(reply.text.size()));
        out += reply.text;
        out += "\r\n";
      }
      break;
    case Reply::Kind::Verbatim:
      if (proto_ >= 3) {
        append_header(out, '=', static_cast<int64_t>(reply.text.size() + 4));
        out += reply.format;
        out.push_back(':');
        out += reply.text;
        out += "\r\n";
      } else {
        append_header(out, '$', static_cast<int64_t>(reply.text.size()));
        out += reply.text;
        out += "\r\n";
      }
      break;
    case Reply::Kind::Pairs:
      if (reply.items.size() % 2 != 0) break;
      if (proto_ >= 3) {
        append_header(out, '*', static_cast<int64_t>(reply.items.size() / 2));
        for (size_t i = 0; i + 1 < reply.items.size(); i += 2) {
          append_header(out, '*', 2);
          append(out, reply.items[i]);
          append(out, reply.items[i + 1]);
        }
      } else {
        append_header(out, '*', static_cast<int64_t>(reply.items.size()));
        for (const auto& item : reply.items) append(out, item);
      }
      break;
    case Reply::Kind::Multi:
      for (const auto& item : reply.items) append(out, item);
      break;
  }
}

Reader::Reader(int fd) : fd_(fd) {}

std::string Reader::read_exact(size_t size) {
  while (buffer_.size() - pos_ < size) {
    if (eof_) throw IOError{};
    char chunk[16384];
    ssize_t n = ::read(fd_, chunk, sizeof(chunk));
    if (n == 0) {
      eof_ = true;
      throw IOError{};
    }
    if (n < 0) {
      if (errno == EINTR) continue;
      throw IOError{};
    }
    buffer_.append(chunk, static_cast<size_t>(n));
  }
  std::string out = buffer_.substr(pos_, size);
  pos_ += size;
  if (pos_ > 1 << 20 && pos_ * 2 > buffer_.size()) {
    buffer_.erase(0, pos_);
    pos_ = 0;
  }
  return out;
}

std::string Reader::read_line() {
  for (;;) {
    auto index = buffer_.find('\n', pos_);
    if (index != std::string::npos) {
      size_t end = index;
      if (end > pos_ && buffer_[end - 1] == '\r') --end;
      std::string line = buffer_.substr(pos_, end - pos_);
      pos_ = index + 1;
      if (line.size() > kMaxInline) throw ProtocolError{"too big inline request"};
      if (pos_ > 1 << 20 && pos_ * 2 > buffer_.size()) {
        buffer_.erase(0, pos_);
        pos_ = 0;
      }
      return line;
    }
    if (buffer_.size() - pos_ > kMaxInline) throw ProtocolError{"too big inline request"};
    if (eof_) throw IOError{};
    char chunk[16384];
    ssize_t n = ::read(fd_, chunk, sizeof(chunk));
    if (n == 0) {
      eof_ = true;
      throw IOError{};
    }
    if (n < 0) {
      if (errno == EINTR) continue;
      throw IOError{};
    }
    buffer_.append(chunk, static_cast<size_t>(n));
  }
}

namespace {
int64_t parse_i64(const std::string& text, const char* what) {
  if (text.empty()) throw ProtocolError{std::string("invalid ") + what + " length"};
  int64_t value = 0;
  auto result = std::from_chars(text.data(), text.data() + text.size(), value);
  if (result.ec != std::errc() || result.ptr != text.data() + text.size()) {
    throw ProtocolError{std::string("invalid ") + what + " length"};
  }
  return value;
}

std::vector<std::string> split_inline(const std::string& line) {
  std::vector<std::string> args;
  std::string current;
  bool in_word = false;
  char quote = 0;
  bool escape = false;

  auto end_word = [&]() {
    if (in_word) {
      args.push_back(current);
      current.clear();
      in_word = false;
    }
  };

  for (size_t i = 0; i < line.size(); ++i) {
    char ch = line[i];
    if (escape) {
      switch (ch) {
        case 'n': current.push_back('\n'); break;
        case 'r': current.push_back('\r'); break;
        case 't': current.push_back('\t'); break;
        case 'b': current.push_back('\b'); break;
        case 'a': current.push_back('\a'); break;
        case 'x': {
          if (i + 2 >= line.size()) throw ProtocolError{"invalid hex escape in request"};
          auto hex = line.substr(i + 1, 2);
          unsigned int value = 0;
          auto result = std::from_chars(hex.data(), hex.data() + hex.size(), value, 16);
          if (result.ec != std::errc()) throw ProtocolError{"invalid hex escape in request"};
          current.push_back(static_cast<char>(value));
          i += 2;
          break;
        }
        default: current.push_back(ch); break;
      }
      escape = false;
      in_word = true;
      continue;
    }
    if (quote != 0) {
      if (ch == quote) {
        quote = 0;
        continue;
      }
      if (ch == '\\' && quote == '"') {
        escape = true;
        continue;
      }
      current.push_back(ch);
      in_word = true;
      continue;
    }
    if (ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n') {
      end_word();
    } else if (ch == '"' || ch == '\'') {
      quote = ch;
      in_word = true;
    } else {
      current.push_back(ch);
      in_word = true;
    }
  }
  if (escape || quote != 0) throw ProtocolError{"unbalanced quotes in request"};
  end_word();
  return args;
}
}  // namespace

bool Reader::read_command(std::vector<std::string>& args) {
  args.clear();
  std::string line;
  try {
    line = read_line();
  } catch (const IOError&) {
    if (pos_ == buffer_.size()) return false;
    throw;
  }
  if (line.empty()) throw ProtocolError{"empty request"};
  if (line[0] != '*') {
    args = split_inline(line);
    return true;
  }
  int64_t count = parse_i64(line.substr(1), "multibulk");
  if (count <= 0 || static_cast<size_t>(count) > kMaxArray) {
    throw ProtocolError{"invalid multibulk length"};
  }
  args.reserve(static_cast<size_t>(count));
  for (int64_t i = 0; i < count; ++i) {
    std::string header = read_line();
    if (header.empty() || header[0] != '$') {
      throw ProtocolError{header.empty() ? "expected '$', got ' '"
                                         : std::string("expected '$', got '") + header[0] + "'"};
    }
    int64_t size = parse_i64(header.substr(1), "bulk");
    if (size < 0 || size > kMaxBulk) throw ProtocolError{"invalid bulk length"};
    std::string data = read_exact(static_cast<size_t>(size) + 2);
    if (data.size() < 2 || data[data.size() - 2] != '\r' || data[data.size() - 1] != '\n') {
      throw ProtocolError{"invalid bulk string terminator"};
    }
    data.resize(static_cast<size_t>(size));
    args.push_back(std::move(data));
  }
  return true;
}

}  // namespace redlite
