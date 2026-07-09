package main

import (
	"os"
	"testing"
)

func TestRun(t *testing.T) {
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer devNull.Close()

	tests := []struct {
		name string
		args []string
		want int
	}{
		{name: "no args prints usage", args: nil, want: 0},
		{name: "help flag", args: []string{"-h"}, want: 0},
		{name: "serve not implemented", args: []string{"serve"}, want: 1},
		{name: "verify not implemented", args: []string{"verify"}, want: 1},
		{name: "reindex not implemented", args: []string{"reindex"}, want: 1},
		{name: "unknown command", args: []string{"bogus"}, want: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := run(tt.args, devNull, devNull); got != tt.want {
				t.Errorf("run(%v) = %d, want %d", tt.args, got, tt.want)
			}
		})
	}
}
