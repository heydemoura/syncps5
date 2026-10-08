/* Installs the home screen shortcut the first time the payload runs.
 *
 * The shortcut is installed by a separate small payload (appicon/), embedded
 * here and handed to the ELF loader on this console, so that the system
 * libraries it needs never end up in the Syncthing process. Build with
 * -DICON_HELPER="path/to/appicon.elf"; without it this file does nothing.
 *
 * Adapted from ps5-tailscale's launcher/homeicon.c (GPLv3). */

#include "homeicon.h"

#ifdef ICON_HELPER

#include "report.h"

#include <fcntl.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>
#include <unistd.h>

#include <arpa/inet.h>
#include <netinet/in.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/time.h>

/* Records that the shortcut has been installed. Once it exists the shortcut
 * is left alone, so a shortcut deleted from the home screen stays deleted.
 * Remove the file to get it back on the next start. */
#define ICON_MARKER SYNCPS5_DIR "/icon-installed"
#define LOADER_PORT 9021

extern const uint8_t icon_helper[];
extern const uint8_t icon_helper_end[];

__asm__(".section .rodata\n"
        ".balign 16\n"
        ".global icon_helper\n"
        "icon_helper:\n"
        ".incbin \"" ICON_HELPER "\"\n"
        ".global icon_helper_end\n"
        "icon_helper_end:\n"
        ".text\n");

void
home_icon_install_once(void) {
  struct sockaddr_in addr = {0};
  struct timeval tv = {1, 0};
  const uint8_t *data = icon_helper;
  size_t left = icon_helper_end - icon_helper;
  char reply[512] = {0};
  size_t got = 0;
  struct stat st;
  int fd;

  if (!stat(ICON_MARKER, &st)) {
    return;
  }
  if ((fd = socket(AF_INET, SOCK_STREAM, 0)) < 0) {
    return;
  }
  addr.sin_family = AF_INET;
  addr.sin_port = htons(LOADER_PORT);
  addr.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
  if (connect(fd, (struct sockaddr *)&addr, sizeof(addr))) {
    report_log("[syncps5] home screen shortcut not installed: no ELF loader on port %d", LOADER_PORT);
    close(fd);
    return;
  }
  while (left > 0) {
    ssize_t n = write(fd, data, left);
    if (n <= 0) {
      close(fd);
      return;
    }
    data += n;
    left -= n;
  }

  /* The helper reports "icon: ok" or what went wrong, then exits, which
   * closes the connection. Give it 20 seconds. */
  setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof(tv));
  for (int tries = 0; tries < 20 && got < sizeof(reply) - 1; tries++) {
    const char *line = strstr(reply, "icon: ");
    if (line && strchr(line, '\n')) {
      break;
    }
    ssize_t n = read(fd, reply + got, sizeof(reply) - 1 - got);
    if (n == 0) {
      break;
    }
    if (n > 0) {
      got += n;
    }
  }
  close(fd);

  if (strstr(reply, "icon: ok")) {
    if ((fd = open(ICON_MARKER, O_WRONLY | O_CREAT | O_TRUNC, 0644)) >= 0) {
      close(fd);
    }
    report_log("[syncps5] Syncthing shortcut added to the Media tab of the home screen");
  } else {
    report_log("[syncps5] home screen shortcut not installed: %s", got ? reply : "no reply from the helper");
  }
}

#else

void
home_icon_install_once(void) {
}

#endif
