// Copyright (C) 2026 syncps5 contributors. GPLv3 (see LICENSE).

package main

// ps5ExitHook runs just before `syncthing serve` exits with the given code.
// The PS5 build sets it to relaunch the payload on restart.
var ps5ExitHook = func(code int) {}
