# V4L2 ABI evidence

`sz.c` carries the V4L2 C layout probe forward from
`research/v4l2/c/sz.c`. It includes `<time.h>` before `<linux/videodev2.h>` for
strict C11 builds and now prints every named field used by the production
mirrors, their sizes and alignments, and all used ioctl/flag constants. It only
prints header definitions; it opens no device and executes no ioctl.

`abi-linux-amd64.txt` is its complete 83-measurement output, generated on
2026-11-17 with GCC 15.2.0 on the x86_64 development host. The installed
`/usr/include/linux/videodev2.h` SHA-256 was
`1ed9b5a4bc200974dcb144c6fb508d754d036ee2ddce9ff64a019863ab1ae477`.

From the repository root, reproduce the fixture with:

```sh
gcc -std=c11 -Wall -Wextra -Werror -o /tmp/autopaws-v4l2-abi v4l2/testdata/sz.c
/tmp/autopaws-v4l2-abi > /tmp/autopaws-v4l2-abi.txt
diff -u v4l2/testdata/abi-linux-amd64.txt /tmp/autopaws-v4l2-abi.txt
```

`TestABIFromLinuxHeaders` compares the Go declarations/constants against every
fixture entry. Compile-time assertions independently pin every named field's
offset, struct size and alignment, including during Linux arm64 cross-builds.
The two supported targets use the LP64 little-endian Linux UAPI representation.

The prototype recorded prior arm64 GCC verification in its comments, but no
archived arm64 C output was present in its reference files. This fixture is
amd64 evidence; cross-compilation checks arm64 against those pinned values and
does not claim a new arm64 C run or any capture-hardware validation.
