/* syncps5 launcher: logging and on-screen notifications.
 * Based on ps5-tailscale's launcher/report.c (GPLv3). */

#include <fcntl.h>
#include <stdarg.h>
#include <stdio.h>
#include <string.h>
#include <time.h>
#include <unistd.h>

#include <sys/stat.h>

#include <ps5/kernel.h>

#include "report.h"

#define LOG_MAX_SIZE (256 * 1024)

typedef struct {
  char unused[45];
  char message[3075];
} notify_request_t;

int sceKernelSendNotificationRequest(int, notify_request_t *, size_t, int);

static int
log_open(void) {
  struct stat st;
  int flags = O_WRONLY | O_CREAT | O_APPEND;

  mkdir(SYNCPS5_DIR, 0755);
  if (!stat(LAUNCHER_LOG, &st) && st.st_size > LOG_MAX_SIZE) {
    flags |= O_TRUNC;
  }
  return open(LAUNCHER_LOG, flags, 0644);
}

static void
log_line(const char *line) {
  char stamp[32] = "";
  time_t now = time(0);
  struct tm tm;
  int fd = log_open();

  if (fd < 0) {
    return;
  }
  if (gmtime_r(&now, &tm)) {
    strftime(stamp, sizeof(stamp), "%Y-%m-%d %H:%M:%S UTC ", &tm);
  }
  write(fd, stamp, strlen(stamp));
  write(fd, line, strlen(line));
  write(fd, "\n", 1);
  close(fd);
}

void
report_begin(void) {
  unsigned fw = kernel_get_fw_version();

  report_log("[syncps5] launcher starting, firmware %x.%02x, pid %d", fw >> 24, (fw >> 16) & 0xff, (int)getpid());
}

void
report_log(const char *fmt, ...) {
  char line[512];
  va_list ap;

  va_start(ap, fmt);
  vsnprintf(line, sizeof(line), fmt, ap);
  va_end(ap);
  fprintf(stdout, "%s\n", line);
  fflush(stdout);
  log_line(line);
}

void
report_notify(const char *fmt, ...) {
  static notify_request_t req;
  va_list ap;

  memset(&req, 0, sizeof(req));
  va_start(ap, fmt);
  vsnprintf(req.message, sizeof(req.message), fmt, ap);
  va_end(ap);
  sceKernelSendNotificationRequest(0, &req, sizeof(req), 0);
}

void
report_fail(const char *fmt, ...) {
  char line[512];
  va_list ap;

  va_start(ap, fmt);
  vsnprintf(line, sizeof(line), fmt, ap);
  va_end(ap);
  report_log("%s", line);
  report_notify("Syncthing did not start:\n%s\nDetails: " LAUNCHER_LOG, line);
}

void
report_capture_stderr(void) {
  int fd = log_open();

  if (fd < 0) {
    return;
  }
  fflush(stderr);
  dup2(fd, 2);
  close(fd);
}
