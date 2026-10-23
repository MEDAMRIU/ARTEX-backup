# Browser and runner seccomp profile

`browser-seccomp.json` derives from Moby's default profile at commit
`4b8214fc4971e06172938a1f1554ccff6647ee80`:
https://github.com/moby/profiles/blob/4b8214fc4971e06172938a1f1554ccff6647ee80/seccomp/default.json

It permits `clone`, `clone3`, `setns`, `unshare`, `chroot`, `mount`, `umount2`
and `pivot_root` for unprivileged user/PID/IPC
namespaces used by Bubblewrap and Chromium. Other default restrictions remain.
Run as an unprivileged user with all capabilities dropped and no-new-privileges.
The profile does not grant network or filesystem authorization. The isolated
runner's namespace mounts and mandatory egress policy provide those boundaries.

This is a deployment candidate until the actual host passes runtime readiness.
The older Playwright sample profile crashed the local container runtime; that
failure is documented in HARDENING.md and must not be treated as a browser pass.
