/* syncps5 home screen shortcut helper.
 *
 * A tiny payload that registers a "Syncthing" app in the Media tab of the
 * PS5 home screen (applicationCategoryType 65536). The app's only content is
 * a deep link to the Syncthing GUI, which the console opens in its browser.
 *
 * The syncps5 launcher embeds this payload and hands it to the ELF loader the
 * first time it runs. It is a separate payload so that the system libraries
 * it needs are never loaded into the Syncthing process, where their threads
 * could receive signals meant for the Go runtime.
 *
 * Prints "icon: ok ..." on success; the launcher looks for that.
 *
 * Build with -DASSET_DIR="dir with param.json and icon0.png" and link, in
 * this order, -lSceIpmi -lSceAppInstUtil -lSceUserService -lSceSystemService
 * (with libSceAppInstUtil alone the payload is never started).
 *
 * Adapted from ps5-tailscale's appicon/main.c (GPLv3). */

#include <errno.h>
#include <fcntl.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#include <sys/stat.h>

#include <ps5/kernel.h>

#ifndef ASSET_DIR
#error "ASSET_DIR must name the folder with param.json and icon0.png"
#endif

#define TITLE_ID "STPS00001"
#define APP_DIR "/user/app/" TITLE_ID

#define INCASSET(name, file)                        \
  __asm__(".section .rodata\n"                      \
          ".balign 16\n"                            \
          ".global " #name "\n" #name ":\n"         \
          ".incbin \"" file "\"\n"                  \
          ".global " #name "_end\n" #name "_end:\n" \
          ".text\n");                               \
  extern const uint8_t name[];                      \
  extern const uint8_t name##_end[];

INCASSET(param_json, ASSET_DIR "/param.json")
INCASSET(icon_png, ASSET_DIR "/icon0.png")

int sceAppInstUtilInitialize(void);
int sceAppInstUtilTerminate(void);
int sceAppInstUtilAppInstallAll(void *);

static int
write_file(const char *path, const uint8_t *data, size_t size) {
  int fd = open(path, O_WRONLY | O_CREAT | O_TRUNC, 0644);

  if (fd < 0) {
    return -1;
  }
  while (size > 0) {
    ssize_t n = write(fd, data, size);
    if (n < 0) {
      if (errno == EINTR) {
        continue;
      }
      close(fd);
      return -1;
    }
    data += n;
    size -= n;
  }
  return close(fd);
}

int
main(void) {
  int (*install_title_dir)(const char *, const char *, void *) = 0;
  pid_t pid = getpid();
  intptr_t rootvnode;
  uint32_t handle;
  int err;

  setvbuf(stdout, 0, _IONBF, 0);

  /* /user/app is only writable as root outside the sandbox. */
  if ((rootvnode = kernel_get_root_vnode())) {
    kernel_set_proc_rootdir(pid, rootvnode);
    kernel_set_proc_jaildir(pid, 0);
  }
  kernel_set_ucred_uid(pid, 0);
  kernel_set_ucred_ruid(pid, 0);
  kernel_set_ucred_svuid(pid, 0);
  kernel_set_ucred_rgid(pid, 0);
  kernel_set_ucred_svgid(pid, 0);

  if ((err = sceAppInstUtilInitialize())) {
    printf("icon: sceAppInstUtilInitialize failed: 0x%08x\n", err);
    return 1;
  }
  mkdir(APP_DIR, 0755);
  mkdir(APP_DIR "/sce_sys", 0755);
  if (write_file(APP_DIR "/sce_sys/param.json", param_json, param_json_end - param_json) ||
      write_file(APP_DIR "/sce_sys/icon0.png", icon_png, icon_png_end - icon_png)) {
    printf("icon: could not write to %s: %s\n", APP_DIR, strerror(errno));
    sceAppInstUtilTerminate();
    return 1;
  }

  /* Register just this title where the firmware supports it; otherwise ask
   * for a rescan of everything under /user/app. */
  if (!kernel_dynlib_handle(-1, "libSceAppInstUtil.sprx", &handle)) {
    install_title_dir = (void *)kernel_dynlib_resolve(-1, handle, "Wudg3Xe3heE");
  }
  if (install_title_dir) {
    err = install_title_dir(TITLE_ID, "/user/app/", 0);
  } else {
    err = sceAppInstUtilAppInstallAll(0);
  }
  sceAppInstUtilTerminate();
  if (err) {
    printf("icon: registering the app failed: 0x%08x\n", err);
    return 1;
  }
  printf("icon: ok (installed %s)\n", TITLE_ID);
  return 0;
}
