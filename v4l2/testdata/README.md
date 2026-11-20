# V4L2 ABI evidence

`sz.c` measures the sizes, alignments, fields and ioctl/flag constants used by the
Go V4L2 declarations against `<linux/videodev2.h>`. It opens no device and
executes no ioctl.

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

The fixture was generated on amd64. Cross-compilation checks arm64 against the
pinned values; it does not validate hardware or replace an arm64 C probe.
