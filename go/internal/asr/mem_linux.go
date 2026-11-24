//go:build linux

package asr

/*
#include <malloc.h>
*/
import "C"

// TrimMemory asks glibc to return freed heap arenas back to the OS.
func TrimMemory() {
	C.malloc_trim(0)
}
