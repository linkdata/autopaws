#include <time.h>
#include <stdio.h>
#include <stddef.h>
#include <linux/videodev2.h>

/* No device I/O or ioctls run here. */
#define S(t) printf(#t ".size=%zu\n" #t ".align=%zu\n", sizeof(struct t), _Alignof(struct t))
#define O(t, f) printf(#t "." #f "=%zu\n", offsetof(struct t, f))
#define I(n) printf(#n "=0x%lx\n", (unsigned long)(n))

int main(void) {
    S(v4l2_capability);
    O(v4l2_capability, driver); O(v4l2_capability, card);
    O(v4l2_capability, bus_info); O(v4l2_capability, version);
    O(v4l2_capability, capabilities); O(v4l2_capability, device_caps);
    S(v4l2_fmtdesc);
    O(v4l2_fmtdesc, index); O(v4l2_fmtdesc, type); O(v4l2_fmtdesc, flags);
    O(v4l2_fmtdesc, description); O(v4l2_fmtdesc, pixelformat); O(v4l2_fmtdesc, mbus_code);
    S(v4l2_pix_format);
    O(v4l2_pix_format, width); O(v4l2_pix_format, height); O(v4l2_pix_format, pixelformat);
    O(v4l2_pix_format, field); O(v4l2_pix_format, bytesperline); O(v4l2_pix_format, sizeimage);
    O(v4l2_pix_format, colorspace); O(v4l2_pix_format, priv); O(v4l2_pix_format, flags);
    O(v4l2_pix_format, ycbcr_enc); O(v4l2_pix_format, quantization); O(v4l2_pix_format, xfer_func);
    S(v4l2_format); O(v4l2_format, type); O(v4l2_format, fmt);
    S(v4l2_requestbuffers);
    O(v4l2_requestbuffers, count); O(v4l2_requestbuffers, type); O(v4l2_requestbuffers, memory);
    O(v4l2_requestbuffers, capabilities); O(v4l2_requestbuffers, flags);
    S(v4l2_buffer);
    O(v4l2_buffer, index); O(v4l2_buffer, type); O(v4l2_buffer, bytesused);
    O(v4l2_buffer, flags); O(v4l2_buffer, field); O(v4l2_buffer, timestamp);
    O(v4l2_buffer, timecode); O(v4l2_buffer, sequence); O(v4l2_buffer, memory);
    O(v4l2_buffer, m); O(v4l2_buffer, length); O(v4l2_buffer, request_fd);
    S(timeval); O(timeval, tv_sec); O(timeval, tv_usec);
    I(VIDIOC_QUERYCAP); I(VIDIOC_ENUM_FMT); I(VIDIOC_S_FMT); I(VIDIOC_REQBUFS);
    I(VIDIOC_QUERYBUF); I(VIDIOC_QBUF); I(VIDIOC_DQBUF); I(VIDIOC_STREAMON); I(VIDIOC_STREAMOFF);
    I(V4L2_BUF_TYPE_VIDEO_CAPTURE); I(V4L2_MEMORY_MMAP); I(VIDEO_MAX_FRAME); I(V4L2_BUF_FLAG_ERROR);
    I(V4L2_CAP_VIDEO_CAPTURE); I(V4L2_CAP_STREAMING); I(V4L2_CAP_DEVICE_CAPS);
    I(V4L2_BUF_FLAG_TIMESTAMP_MASK); I(V4L2_BUF_FLAG_TIMESTAMP_UNKNOWN);
    I(V4L2_BUF_FLAG_TIMESTAMP_MONOTONIC); I(V4L2_BUF_FLAG_TIMESTAMP_COPY);
    I(V4L2_BUF_FLAG_TSTAMP_SRC_MASK); I(V4L2_BUF_FLAG_TSTAMP_SRC_EOF); I(V4L2_BUF_FLAG_TSTAMP_SRC_SOE);
    I(V4L2_PIX_FMT_MJPEG);
    return 0;
}
