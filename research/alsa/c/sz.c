#include <stdio.h>
#include <stddef.h>
#include <sys/ioctl.h>
#include <sound/asound.h>
#define P(x) printf("%-34s 0x%08lx\n", #x, (unsigned long)(x))
#define S(x) printf("sizeof %-27s %zu\n", #x, sizeof(x))
#define O(t,f) printf("  off %s.%s %zu\n", #t, #f, offsetof(t,f))
int main(){
P(SNDRV_PCM_IOCTL_PVERSION);P(SNDRV_PCM_IOCTL_INFO);P(SNDRV_PCM_IOCTL_HW_REFINE);P(SNDRV_PCM_IOCTL_HW_PARAMS);P(SNDRV_PCM_IOCTL_HW_FREE);
P(SNDRV_PCM_IOCTL_SW_PARAMS);P(SNDRV_PCM_IOCTL_STATUS);P(SNDRV_PCM_IOCTL_PREPARE);P(SNDRV_PCM_IOCTL_START);P(SNDRV_PCM_IOCTL_DROP);
P(SNDRV_PCM_IOCTL_READI_FRAMES);P(SNDRV_PCM_IOCTL_USER_PVERSION);P(SNDRV_PCM_IOCTL_TTSTAMP);P(SNDRV_PCM_IOCTL_RESUME);P(SNDRV_PCM_IOCTL_DELAY);
S(struct snd_pcm_hw_params);S(struct snd_mask);S(struct snd_interval);S(struct snd_pcm_sw_params);S(struct snd_xferi);S(struct snd_pcm_info);S(struct snd_pcm_status);
O(struct snd_pcm_hw_params,flags);O(struct snd_pcm_hw_params,masks);O(struct snd_pcm_hw_params,mres);O(struct snd_pcm_hw_params,intervals);O(struct snd_pcm_hw_params,ires);
O(struct snd_pcm_hw_params,rmask);O(struct snd_pcm_hw_params,cmask);O(struct snd_pcm_hw_params,info);O(struct snd_pcm_hw_params,msbits);O(struct snd_pcm_hw_params,rate_num);
O(struct snd_pcm_hw_params,rate_den);O(struct snd_pcm_hw_params,fifo_size);
O(struct snd_pcm_sw_params,tstamp_mode);O(struct snd_pcm_sw_params,avail_min);O(struct snd_pcm_sw_params,xfer_align);O(struct snd_pcm_sw_params,start_threshold);
O(struct snd_pcm_sw_params,stop_threshold);O(struct snd_pcm_sw_params,silence_threshold);O(struct snd_pcm_sw_params,silence_size);O(struct snd_pcm_sw_params,boundary);
O(struct snd_pcm_sw_params,proto);O(struct snd_pcm_sw_params,tstamp_type);O(struct snd_pcm_sw_params,reserved);
O(struct snd_xferi,result);O(struct snd_xferi,buf);O(struct snd_xferi,frames);
printf("PARAM ACCESS=%d FORMAT=%d SUBFORMAT=%d FIRST_INTERVAL=%d SAMPLE_BITS=%d FRAME_BITS=%d CHANNELS=%d RATE=%d PERIOD_TIME=%d PERIOD_SIZE=%d PERIOD_BYTES=%d PERIODS=%d BUFFER_TIME=%d BUFFER_SIZE=%d BUFFER_BYTES=%d TICK_TIME=%d LAST_INTERVAL=%d\n",
SNDRV_PCM_HW_PARAM_ACCESS,SNDRV_PCM_HW_PARAM_FORMAT,SNDRV_PCM_HW_PARAM_SUBFORMAT,SNDRV_PCM_HW_PARAM_FIRST_INTERVAL,SNDRV_PCM_HW_PARAM_SAMPLE_BITS,SNDRV_PCM_HW_PARAM_FRAME_BITS,SNDRV_PCM_HW_PARAM_CHANNELS,SNDRV_PCM_HW_PARAM_RATE,SNDRV_PCM_HW_PARAM_PERIOD_TIME,SNDRV_PCM_HW_PARAM_PERIOD_SIZE,SNDRV_PCM_HW_PARAM_PERIOD_BYTES,SNDRV_PCM_HW_PARAM_PERIODS,SNDRV_PCM_HW_PARAM_BUFFER_TIME,SNDRV_PCM_HW_PARAM_BUFFER_SIZE,SNDRV_PCM_HW_PARAM_BUFFER_BYTES,SNDRV_PCM_HW_PARAM_TICK_TIME,SNDRV_PCM_HW_PARAM_LAST_INTERVAL);
printf("ACCESS_RW_INTERLEAVED=%d FORMAT_S16_LE=%d SUBFORMAT_STD=%d SNDRV_PCM_VERSION=0x%x\n",SNDRV_PCM_ACCESS_RW_INTERLEAVED,SNDRV_PCM_FORMAT_S16_LE,SNDRV_PCM_SUBFORMAT_STD,SNDRV_PCM_VERSION);
}
