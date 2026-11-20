# Linux UAPI evidence

`sz.c` measures the LP64 declarations and constants used by this package against
`<sound/asound.h>`. `abi-linux-amd64.txt` records 89 measurements from GCC 15.2.0
on the development host, with Linux UAPI headers whose SHA-256 was
`8386524aad49b022dcb4f49b947a47b7caf7cbbc3bdb05baf718dfdb98585a01`.
No PCM was opened to collect ABI evidence.

The recorded mask/interval representation includes the interval bitfield word
at offset 8. The C probe sets all four bits and records their combined value.

From the repository root, reproduce on a Linux host with development headers:

```sh
gcc -std=c11 -Wall -Wextra -Werror alsa/testdata/sz.c -o /tmp/autopaws-alsa-abi
/tmp/autopaws-alsa-abi > /tmp/autopaws-alsa-abi.txt
diff -u alsa/testdata/abi-linux-amd64.txt /tmp/autopaws-alsa-abi.txt
```

Compile-time assertions and tests compare every recorded named field, alignment,
size and constant. The fixture was generated on amd64; arm64 cross-compilation
checks the pinned layout without validating hardware or running an arm64 C probe.
