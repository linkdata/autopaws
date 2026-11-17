# Linux UAPI evidence

`sz.c` measures the LP64 declarations and constants used by this package against
`<sound/asound.h>`. `abi-linux-amd64.txt` records 89 measurements from GCC 15.2.0
on the development host, with Linux UAPI headers whose SHA-256 was
`8386524aad49b022dcb4f49b947a47b7caf7cbbc3bdb05baf718dfdb98585a01`.
No PCM was opened to collect ABI evidence.

The recorded mask/interval representation includes the interval bitfield word
at offset 8. The C probe sets all four bits and records their combined value.
`snd_xferi.buf` is mirrored as `unsafe.Pointer`, preserving Go pointer liveness
through the ioctl. Pointer conversion to `uintptr` happens only in the actual
syscall call.

Reproduce on a Linux host with development headers:

```sh
gcc -std=c11 -Wall -Wextra -Werror sz.c -o /tmp/autopaws-alsa-abi
/tmp/autopaws-alsa-abi
```

Compile-time assertions and tests compare every recorded named field, alignment,
size and constant. Linux arm64 cross-compilation checks Go's layout; no archived
arm64 C probe output is claimed here, and this is not hardware validation.
