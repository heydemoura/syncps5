/* syncps5: PS5 payload entry point.
 *
 * Prepares the process (privileges, scheduling, single instance, optional
 * self-install from the sender's connection), then hands control to the
 * embedded Syncthing binary through goload.
 *
 * Build with -DGO_IMAGE="path/to/syncthing.bin".
 *
 * The scheduling logic is taken from ps5-tailscale's launcher (GPLv3). */

#include <sys/types.h>
#include <sys/socket.h>
#include <arpa/inet.h>
#include <errno.h>
#include <fcntl.h>
#include <ifaddrs.h>
#include <netinet/in.h>
#include <poll.h>
#include <signal.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#include <sys/mman.h>
#include <sys/stat.h>
#include <sys/syscall.h>
#include <sys/sysctl.h>

#include <ps5/kernel.h>

#include "goload.h"
#include "report.h"

#ifndef GO_IMAGE
#error "GO_IMAGE must name the Go binary to embed"
#endif

#ifndef SYNCPS5_VERSION
#define SYNCPS5_VERSION "dev"
#endif

/* How many OS threads may run Go code at once. The console has 16 logical
 * cores; this is a background service and games need them more. */
#ifndef GO_MAXPROCS
#define GO_MAXPROCS "4"
#endif

/* Port of the Syncthing GUI (and of the first-run setup page). */
#ifndef SYNCPS5_GUI_PORT
#define SYNCPS5_GUI_PORT "8384"
#endif

#define PROC_NAME "syncps5"
#define INSTALLED_ELF SYNCPS5_DIR "/syncps5.elf"
#define INSTALL_MAGIC "SYNCPS5INSTALL01"

extern const uint8_t go_image[];
extern const uint8_t go_image_end[];

__asm__(".section .rodata\n"
        ".balign 0x4000\n"
        ".global go_image\n"
        "go_image:\n"
        ".incbin \"" GO_IMAGE "\"\n"
        ".global go_image_end\n"
        "go_image_end:\n"
        ".text\n");

/* ------------------------------------------------------------------------ */
/* Scheduling                                                               */
/* ------------------------------------------------------------------------ */

#define SYS_rtprio_thread_ 466
#define RTP_LOOKUP 0
#define RTP_SET 1
#define RTP_PRIO_REALTIME 2 /* round-robin */
#define RTP_PRIO_NORMAL 3   /* time-sharing */
#define PS5_PRIO_LOWEST 767

struct rtprio_ {
  unsigned short type;
  unsigned short prio;
};

static long
raw_syscall3(long n, long a, long b, long c) {
  unsigned char failed;
  __asm__ volatile("syscall; setc %1" : "+a"(n), "=q"(failed) : "D"(a), "S"(b), "d"(c) : "rcx", "r11", "memory");
  return failed ? -n : n;
}

/* Payload threads start as FIFO threads at priority 700. A FIFO thread that
 * never blocks is never descheduled, so a busy Go runtime could freeze the
 * console. Move to time-sharing at the lowest priority before any Go thread
 * exists; threads created later inherit it. The kernel silently ignores
 * out-of-range values, so the result is read back. */
static int
leave_realtime_class(void) {
  static const struct rtprio_ choices[] = {
      {RTP_PRIO_NORMAL, PS5_PRIO_LOWEST},
      {RTP_PRIO_REALTIME, PS5_PRIO_LOWEST},
  };
  struct rtprio_ before = {0}, after = {0};

  raw_syscall3(SYS_rtprio_thread_, RTP_LOOKUP, 0, (long)&before);
  for (size_t i = 0; i < sizeof(choices) / sizeof(choices[0]); i++) {
    struct rtprio_ want = choices[i];
    raw_syscall3(SYS_rtprio_thread_, RTP_SET, 0, (long)&want);
    raw_syscall3(SYS_rtprio_thread_, RTP_LOOKUP, 0, (long)&after);
    if (after.type == want.type && after.prio == want.prio) {
      report_log("[syncps5] scheduling class %u/%u -> %u/%u", before.type, before.prio, after.type, after.prio);
      return 0;
    }
  }
  return -1;
}

/* ------------------------------------------------------------------------ */
/* Single instance                                                          */
/* ------------------------------------------------------------------------ */

/* Collect the pids of other processes whose main thread is named PROC_NAME.
 * Same kinfo_proc walk as ps5-payload-dev/elfldr. */
static int
find_other_instances(pid_t *pids, int max) {
  int mib[4] = {CTL_KERN, KERN_PROC, KERN_PROC_PROC, 0};
  pid_t mypid = getpid();
  size_t size = 0;
  uint8_t *buf;
  int n = 0;

  if (sysctl(mib, 4, 0, &size, 0, 0)) {
    return 0;
  }
  size += 16 * 1024;
  if (!(buf = malloc(size))) {
    return 0;
  }
  if (sysctl(mib, 4, buf, &size, 0, 0)) {
    free(buf);
    return 0;
  }
  for (uint8_t *p = buf; p < buf + size && n < max;) {
    int ki_structsize = *(int *)p;
    pid_t ki_pid = *(pid_t *)&p[72];
    const char *ki_tdname = (const char *)&p[447];

    if (ki_structsize <= 0) {
      break;
    }
    if (ki_pid != mypid && !strcmp(ki_tdname, PROC_NAME)) {
      pids[n++] = ki_pid;
    }
    p += ki_structsize;
  }
  free(buf);
  return n;
}

/* Ask a running instance to stop (Syncthing shuts down cleanly on SIGTERM),
 * and kill it if it has not gone after a while. */
static void
stop_other_instances(void) {
  pid_t pids[8];
  int n = find_other_instances(pids, 8);

  if (!n) {
    return;
  }
  for (int i = 0; i < n; i++) {
    report_log("[syncps5] stopping running instance, pid %d", (int)pids[i]);
    kill(pids[i], SIGTERM);
  }
  for (int t = 0; t < 100 && find_other_instances(pids, 8); t++) {
    usleep(100 * 1000);
  }
  n = find_other_instances(pids, 8);
  for (int i = 0; i < n; i++) {
    report_log("[syncps5] instance pid %d did not stop, killing it", (int)pids[i]);
    kill(pids[i], SIGKILL);
  }
  if (n) {
    sleep(1);
  }
}

/* ------------------------------------------------------------------------ */
/* Self-install                                                             */
/* ------------------------------------------------------------------------ */

static int
read_full(int fd, void *buf, size_t len, int timeout_ms) {
  uint8_t *p = buf;

  while (len) {
    struct pollfd pfd = {.fd = fd, .events = POLLIN};
    if (poll(&pfd, 1, timeout_ms) <= 0) {
      return -1;
    }
    ssize_t r = read(fd, p, len);
    if (r <= 0) {
      return -1;
    }
    p += r;
    len -= r;
  }
  return 0;
}

/* The deploy tool may append INSTALL_MAGIC, a 64-bit little endian length
 * and a copy of the payload after the ELF it sends to the loader. The loader
 * reads only the ELF, so the rest arrives on our stdin. Saving that copy lets
 * Syncthing relaunch itself (restart from the GUI) and lets an autoloader
 * start it at boot. */
static void
install_from_stdin(void) {
  char magic[sizeof(INSTALL_MAGIC) - 1];
  uint64_t len = 0;
  uint8_t *data;
  int fd;

  if (read_full(0, magic, sizeof(magic), 1500) || memcmp(magic, INSTALL_MAGIC, sizeof(magic))) {
    return;
  }
  if (read_full(0, &len, sizeof(len), 5000) || len < 64 || len > (256ull << 20)) {
    report_log("[syncps5] install: bad length");
    return;
  }
  if (!(data = malloc(len))) {
    report_log("[syncps5] install: out of memory");
    return;
  }
  if (read_full(0, data, len, 30000) || memcmp(data, "\177ELF", 4)) {
    report_log("[syncps5] install: could not read the payload copy");
    free(data);
    return;
  }
  fd = open(INSTALLED_ELF ".tmp", O_WRONLY | O_CREAT | O_TRUNC, 0644);
  if (fd < 0) {
    report_log("[syncps5] install: open: %s", strerror(errno));
    free(data);
    return;
  }
  for (size_t off = 0; off < len;) {
    ssize_t w = write(fd, data + off, len - off);
    if (w <= 0) {
      report_log("[syncps5] install: write: %s", strerror(errno));
      close(fd);
      free(data);
      return;
    }
    off += w;
  }
  fsync(fd);
  close(fd);
  free(data);
  if (rename(INSTALLED_ELF ".tmp", INSTALLED_ELF)) {
    report_log("[syncps5] install: rename: %s", strerror(errno));
    return;
  }
  report_log("[syncps5] installed %llu bytes to " INSTALLED_ELF, (unsigned long long)len);
}

/* ------------------------------------------------------------------------ */

static void
local_ip(char *ip, size_t size) {
  struct ifaddrs *ifaddr;

  snprintf(ip, size, "127.0.0.1");
  if (getifaddrs(&ifaddr) == -1) {
    return;
  }
  for (struct ifaddrs *ifa = ifaddr; ifa; ifa = ifa->ifa_next) {
    if (!ifa->ifa_addr || ifa->ifa_addr->sa_family != AF_INET || !strncmp("lo", ifa->ifa_name, 2)) {
      continue;
    }
    char tmp[INET_ADDRSTRLEN];
    inet_ntop(AF_INET, &((struct sockaddr_in *)ifa->ifa_addr)->sin_addr, tmp, sizeof(tmp));
    if (strncmp("0.", tmp, 2)) {
      snprintf(ip, size, "%s", tmp);
    }
  }
  freeifaddrs(ifaddr);
}

int
main(int argc, char **argv) {
  pid_t pid = getpid();
  intptr_t rootvnode;
  char ip[INET_ADDRSTRLEN];

  /* Run as root outside the sandbox so /data, /user and the network are
   * reachable. */
  if ((rootvnode = kernel_get_root_vnode())) {
    kernel_set_proc_rootdir(pid, rootvnode);
    kernel_set_proc_jaildir(pid, 0);
  }
  kernel_set_ucred_uid(pid, 0);
  kernel_set_ucred_ruid(pid, 0);
  kernel_set_ucred_svuid(pid, 0);
  kernel_set_ucred_rgid(pid, 0);
  kernel_set_ucred_svgid(pid, 0);

  mkdir(SYNCPS5_DIR, 0755);
  mkdir(SYNCPS5_DIR "/tmp", 0755);
  report_begin();
  report_log("[syncps5] version " SYNCPS5_VERSION);

  install_from_stdin();
  stop_other_instances();
  syscall(SYS_thr_set_name, -1, PROC_NAME);

  if (leave_realtime_class()) {
    report_fail("could not lower the scheduling priority; not starting, "
                "because a busy Syncthing could then freeze the console");
    return 1;
  }

  local_ip(ip, sizeof(ip));
  report_log("[syncps5] Syncthing GUI: http://%s:" SYNCPS5_GUI_PORT "  logs: nc %s 8385", ip, ip);
  report_notify("Syncthing starting\nhttp://%s:" SYNCPS5_GUI_PORT, ip);

  static char *envp[] = {
      "HOME=" SYNCPS5_DIR,
      "TMPDIR=" SYNCPS5_DIR "/tmp",
      "STHOMEDIR=" SYNCPS5_DIR "/home",
      "STMONITORED=1", /* no monitor process: the PS5 cannot fork/exec */
      "STGUIADDRESS=0.0.0.0:" SYNCPS5_GUI_PORT,
      "STNORESTART=1",
      "STNOUPGRADE=1",
      "STNOBROWSER=1",
      "GOMAXPROCS=" GO_MAXPROCS,
      "GOMEMLIMIT=768MiB",
      "SYNCPS5=" SYNCPS5_VERSION,
      0,
  };

  return goload_run(go_image, (size_t)(go_image_end - go_image), argv, envp);
}
