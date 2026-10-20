package main

import (
	"github.com/openabstractions/abstraction-provider-kit/go/internal/personfolders"
	"os"
	"testing"
)

func TestMain(m *testing.M) { os.Exit(personfolders.Main(m)) }
