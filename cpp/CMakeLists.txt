cmake_minimum_required(VERSION 3.20)
project(redlite_cpp LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

if(NOT CMAKE_BUILD_TYPE)
  set(CMAKE_BUILD_TYPE Release)
endif()

add_compile_options(-Wall -Wextra -Wpedantic)

add_library(redlite_core
  src/resp.cpp
  src/store.cpp
  src/server.cpp
  src/commands_core.cpp
  src/commands_string.cpp
  src/commands_hash.cpp
  src/commands_list.cpp
  src/commands_set.cpp
  src/commands_zset.cpp
)

target_include_directories(redlite_core PUBLIC src)
find_package(Threads REQUIRED)
target_link_libraries(redlite_core PUBLIC Threads::Threads)

add_executable(redlite src/main.cpp)
target_link_libraries(redlite PRIVATE redlite_core)

enable_testing()
add_executable(redlite_tests tests/tests.cpp)
target_link_libraries(redlite_tests PRIVATE redlite_core)
add_test(NAME redlite_tests COMMAND redlite_tests)
