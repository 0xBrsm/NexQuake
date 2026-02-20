package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"reflect"
	"testing"
)

func TestFNV64aHex_KnownVectors(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "", want: "cbf29ce484222325"},
		{in: "hello", want: "a430d84680aabd0b"},
	}

	for _, tt := range tests {
		got := FNV64aHex(tt.in)
		if got != tt.want {
			t.Fatalf("FNV64aHex(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func writeTestPak(t *testing.T, pakPath string, files map[string][]byte) {
	t.Helper()

	type dirEnt struct {
		name   string
		offset int32
		size   int32
	}

	var data bytes.Buffer
	data.Grow(12)
	data.Write(make([]byte, 12)) // header placeholder

	entries := make([]dirEnt, 0, len(files))
	for name, content := range files {
		off := int32(data.Len())
		data.Write(content)
		entries = append(entries, dirEnt{name: name, offset: off, size: int32(len(content))})
	}

	dirOffset := int32(data.Len())
	for _, e := range entries {
		nameBytes := make([]byte, 56)
		copy(nameBytes, []byte(e.name))
		data.Write(nameBytes)

		var tmp [8]byte
		binary.LittleEndian.PutUint32(tmp[0:4], uint32(e.offset))
		binary.LittleEndian.PutUint32(tmp[4:8], uint32(e.size))
		data.Write(tmp[:])
	}
	dirLen := int32(len(entries) * 64)

	out := data.Bytes()
	copy(out[0:4], []byte("PACK"))
	binary.LittleEndian.PutUint32(out[4:8], uint32(dirOffset))
	binary.LittleEndian.PutUint32(out[8:12], uint32(dirLen))

	if err := os.WriteFile(pakPath, out, 0o644); err != nil {
		t.Fatalf("write pak: %v", err)
	}
}

func TestApplyOperatorConsoleTimestampEnv(t *testing.T) {
	t.Run("accepts 1", func(t *testing.T) {
		t.Setenv("CONSOLE_TIMESTAMPS", "1")
		setOperatorConsoleTimestamps(false)
		applyOperatorConsoleTimestampEnv()
		if !operatorConsoleTimestampsEnabled() {
			t.Fatalf("expected operator console timestamps enabled for 1")
		}
	})

	t.Run("accepts 0", func(t *testing.T) {
		t.Setenv("CONSOLE_TIMESTAMPS", "0")
		setOperatorConsoleTimestamps(true)
		applyOperatorConsoleTimestampEnv()
		if operatorConsoleTimestampsEnabled() {
			t.Fatalf("expected operator console timestamps disabled for 0")
		}
	})

	t.Run("rejects non 0-1 values", func(t *testing.T) {
		t.Setenv("CONSOLE_TIMESTAMPS", "on")
		setOperatorConsoleTimestamps(false)
		applyOperatorConsoleTimestampEnv()
		if !operatorConsoleTimestampsEnabled() {
			t.Fatalf("expected default enabled state for invalid value")
		}
	})
}

func TestGetEnvBool01(t *testing.T) {
	t.Run("uses default when unset", func(t *testing.T) {
		t.Setenv("CL_SMENU", "")
		if getEnvBool01("CL_SMENU", true) != true {
			t.Fatalf("expected default true when unset")
		}
		if getEnvBool01("CL_SMENU", false) != false {
			t.Fatalf("expected default false when unset")
		}
	})

	t.Run("accepts 1", func(t *testing.T) {
		t.Setenv("CL_SMENU", "1")
		if !getEnvBool01("CL_SMENU", false) {
			t.Fatalf("expected true for 1")
		}
	})

	t.Run("accepts 0", func(t *testing.T) {
		t.Setenv("CL_SMENU", "0")
		if getEnvBool01("CL_SMENU", true) {
			t.Fatalf("expected false for 0")
		}
	})

	t.Run("invalid uses default", func(t *testing.T) {
		t.Setenv("CL_SMENU", "on")
		if !getEnvBool01("CL_SMENU", true) {
			t.Fatalf("expected true default for invalid")
		}
		if getEnvBool01("CL_SMENU", false) {
			t.Fatalf("expected false default for invalid")
		}
	})
}

func TestGetEnvIntMin(t *testing.T) {
	t.Run("accepts zero when min is zero", func(t *testing.T) {
		t.Setenv("CL_CONCURRENCY", "0")
		if got := getEnvIntMin("CL_CONCURRENCY", 16, 0); got != 0 {
			t.Fatalf("getEnvIntMin()=%d want=0", got)
		}
	})

	t.Run("rejects below-min values", func(t *testing.T) {
		t.Setenv("CL_CONCURRENCY", "-1")
		if got := getEnvIntMin("CL_CONCURRENCY", 16, 0); got != 16 {
			t.Fatalf("getEnvIntMin()=%d want=16", got)
		}
	})
}

func TestGetEnvArgs(t *testing.T) {
	t.Run("uses default when unset", func(t *testing.T) {
		t.Setenv("CL_SEND_ARGS", "")
		want := []string{"-nosound", "+skill", "3"}
		got := getEnvArgs("CL_SEND_ARGS", want)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("getEnvArgs()=%v want=%v", got, want)
		}
	})

	t.Run("parses shell args with plus commands", func(t *testing.T) {
		t.Setenv("CL_SEND_ARGS", "-nosound +skill 3 +exec autoexec.cfg")
		want := []string{"-nosound", "+skill", "3", "+exec", "autoexec.cfg"}
		got := getEnvArgs("CL_SEND_ARGS", nil)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("getEnvArgs()=%v want=%v", got, want)
		}
	})

	t.Run("keeps quoted values", func(t *testing.T) {
		t.Setenv("CL_SEND_ARGS", `+name "Player One" +skill 3`)
		want := []string{"+name", "Player One", "+skill", "3"}
		got := getEnvArgs("CL_SEND_ARGS", nil)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("getEnvArgs()=%v want=%v", got, want)
		}
	})

	t.Run("invalid value uses default", func(t *testing.T) {
		t.Setenv("CL_SEND_ARGS", `"unterminated`)
		want := []string{"-window"}
		got := getEnvArgs("CL_SEND_ARGS", want)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("getEnvArgs()=%v want=%v", got, want)
		}
	})
}
