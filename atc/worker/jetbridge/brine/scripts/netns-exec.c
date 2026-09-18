// Linux test launcher: create owned namespaces before configuring any address.
// exec preserves the adapter PID, signals and inherited protocol descriptors.
#define _GNU_SOURCE
#include <arpa/inet.h>
#include <errno.h>
#include <net/if.h>
#include <pwd.h>
#include <sched.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ioctl.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <unistd.h>

static void fail(const char *what) { perror(what); exit(1); }
static void write_map(const char *path, const char *value) {
    FILE *f = fopen(path, "w");
    if (!f) fail("open own namespace map");
    if (fputs(value, f) == EOF || fclose(f)) fail("write own namespace map");
}
static void private_namespaces(void) {
    struct stat net_before, net_after, user_before, user_after;
    uid_t uid = getuid();
    gid_t gid = getgid();
    uid_t inner_uid = uid;
    gid_t inner_gid = gid;
    if (uid == 0 || gid == 0) {
        struct passwd *unprivileged = getpwnam("nobody");
        if (!unprivileged || unprivileged->pw_uid == 0 || unprivileged->pw_gid == 0) {
            fprintf(stderr, "a non-root nobody identity is required\n");
            exit(2);
        }
        inner_uid = unprivileged->pw_uid;
        inner_gid = unprivileged->pw_gid;
    }
    if (stat("/proc/self/ns/net", &net_before) || stat("/proc/self/ns/user", &user_before))
        fail("read original namespaces");
    if (unshare(CLONE_NEWUSER | CLONE_NEWNET)) fail("create private namespaces");
    write_map("/proc/self/setgroups", "deny\n");
    char mapping[80];
    snprintf(mapping, sizeof mapping, "%lu %lu 1\n", (unsigned long)inner_uid, (unsigned long)uid);
    write_map("/proc/self/uid_map", mapping);
    snprintf(mapping, sizeof mapping, "%lu %lu 1\n", (unsigned long)inner_gid, (unsigned long)gid);
    write_map("/proc/self/gid_map", mapping);
    if (stat("/proc/self/ns/net", &net_after) || stat("/proc/self/ns/user", &user_after))
        fail("read private namespaces");
    if ((net_before.st_dev == net_after.st_dev && net_before.st_ino == net_after.st_ino) ||
        (user_before.st_dev == user_after.st_dev && user_before.st_ino == user_after.st_ino)) {
        fprintf(stderr, "refusing to configure unchanged namespaces\n");
        exit(2);
    }
}
int main(int argc, char **argv) {
    if (argc < 2) { fprintf(stderr, "command required\n"); return 2; }
    private_namespaces();
    int fd = socket(AF_INET, SOCK_DGRAM, 0);
    if (fd < 0) fail("socket");
    struct ifreq req = {0};
    strcpy(req.ifr_name, "lo");
    if (ioctl(fd, SIOCGIFFLAGS, &req)) fail("read loopback flags");
    req.ifr_flags |= IFF_UP;
    if (ioctl(fd, SIOCSIFFLAGS, &req)) fail("bring up private loopback");
    const char *addresses[] = {"10.203.0.1", "10.203.0.2"};
    for (int i = 0; i < 2; i++) {
        memset(&req, 0, sizeof req);
        snprintf(req.ifr_name, sizeof req.ifr_name, "lo:brine%d", i);
        struct sockaddr_in *addr = (struct sockaddr_in *)&req.ifr_addr;
        addr->sin_family = AF_INET;
        if (inet_pton(AF_INET, addresses[i], &addr->sin_addr) != 1) fail("parse address");
        if (ioctl(fd, SIOCSIFADDR, &req)) fail("assign private address");
        addr->sin_addr.s_addr = htonl(0xffffffff);
        if (ioctl(fd, SIOCSIFNETMASK, &req)) fail("assign private netmask");
    }
    close(fd);
    if (setenv("BRINE_PEER_ADDRESS_1", addresses[0], 1) ||
        setenv("BRINE_PEER_ADDRESS_2", addresses[1], 1)) fail("publish private addresses");
    execvp(argv[1], argv + 1);
    fail("exec command");
}
