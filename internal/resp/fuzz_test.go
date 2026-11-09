package resp

import (
	"bytes"
	"testing"
)

// FuzzReadCommand checks that arbitrary bytes can never panic the parser or
// make it allocate past its configured limits.
func FuzzReadCommand(f *testing.F) {
	seeds := []string{
		"*1\r\n$4\r\nPING\r\n",
		"PING\r\n",
		"*2\r\n$3\r\nGET\r\n$1\r\nk\r\n",
		"*1\r\n$0\r\n\r\n",
		"*3\r\n$3\r\nSET\r\n$1\r\nk\r\n$5\r\nvalue\r\n",
		"ECHO \"a\\x41b\"\r\n",
		"*2\r\n$3\r\nGET\r\n$5\r\nab",
	}
	for _, seed := range seeds {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		reader := NewReader(bytes.NewReader(data))
		reader.MaxArray = 1024
		reader.MaxBulk = 1 << 20
		reader.MaxInline = 4096

		for i := 0; i < 16; i++ {
			args, err := reader.ReadCommand()
			if err != nil {
				return
			}
			if len(args) > reader.MaxArray {
				t.Fatalf("parser returned %d arguments, limit is %d", len(args), reader.MaxArray)
			}
			for _, arg := range args {
				if int64(len(arg)) > reader.MaxBulk {
					t.Fatalf("argument of %d bytes exceeds the bulk limit", len(arg))
				}
			}
		}
	})
}
