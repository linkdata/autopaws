#include <stdio.h>
#include <stddef.h>
#include <linux/videodev2.h>
#define S(t) printf("%-28s size=%zu\n", #t, sizeof(struct t))
#define O(t,f) printf("  %s.%s off=%zu\n", #t, #f, offsetof(struct t,f))
#define I(n) printf("%-28s 0x%08lx\n", #n, (unsigned long)(n))
int main(){
S(v4l2_capability); O(v4l2_capability,capabilities); O(v4l2_capability,device_caps);
S(v4l2_fmtdesc); O(v4l2_fmtdesc,pixelformat); O(v4l2_fmtdesc,mbus_code);
S(v4l2_frmsizeenum); O(v4l2_frmsizeenum,type); O(v4l2_frmsizeenum,discrete);
S(v4l2_frmivalenum); O(v4l2_frmivalenum,type); O(v4l2_frmivalenum,discrete);
S(v4l2_pix_format); S(v4l2_format); O(v4l2_format,fmt);
S(v4l2_streamparm); O(v4l2_streamparm,parm); S(v4l2_captureparm); O(v4l2_captureparm,timeperframe);O(v4l2_captureparm,readbuffers);
S(v4l2_requestbuffers);
S(v4l2_buffer); O(v4l2_buffer,bytesused); O(v4l2_buffer,flags); O(v4l2_buffer,field); O(v4l2_buffer,timestamp); O(v4l2_buffer,timecode); O(v4l2_buffer,sequence); O(v4l2_buffer,memory); O(v4l2_buffer,m); O(v4l2_buffer,length); O(v4l2_buffer,reserved2); O(v4l2_buffer,request_fd);
S(v4l2_timecode); printf("timeval %zu\n", sizeof(struct timeval));
I(VIDIOC_QUERYCAP);I(VIDIOC_ENUM_FMT);I(VIDIOC_G_FMT);I(VIDIOC_S_FMT);I(VIDIOC_TRY_FMT);I(VIDIOC_G_PARM);I(VIDIOC_S_PARM);
I(VIDIOC_REQBUFS);I(VIDIOC_QUERYBUF);I(VIDIOC_QBUF);I(VIDIOC_DQBUF);I(VIDIOC_STREAMON);I(VIDIOC_STREAMOFF);
I(VIDIOC_ENUM_FRAMESIZES);I(VIDIOC_ENUM_FRAMEINTERVALS);
I(V4L2_CAP_VIDEO_CAPTURE);I(V4L2_CAP_META_CAPTURE);I(V4L2_CAP_STREAMING);I(V4L2_CAP_DEVICE_CAPS);I(V4L2_PIX_FMT_MJPEG);I(V4L2_PIX_FMT_YUYV);
}
