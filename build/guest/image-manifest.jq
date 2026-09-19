{
  api_version: "epoch-image/v1",
  id: $image_id,
  architecture: "x86_64",
  kernel: {
    path: "kernel.elf", sha256: $kh, size_bytes: $ks,
    source_identity: $source, checksum_origin: "locally-observed"
  },
  rootfs: {
    path: "rootfs.ext4", sha256: $rh, size_bytes: $rs,
    source_identity: $source, checksum_origin: "locally-observed"
  },
  firecracker_version: "1.16.1",
  guest_agent: {version: "0.1.0-dev", protocol_version: "epoch-guest/v1", sha256: $ah},
  workload: {
    id: $workload_id, version: $workload_version, manifest_path: "workload.json",
    manifest_sha256: $wh
  },
  recipe: {
    revision: "epoch-systemd-guest/v3",
    transformations: [
      $transformation,
      ("Recorded preparation-inputs.json SHA-256 " + $prep),
      ("Installed local CGO-disabled guest agent and declared workload " + $workload_id),
      "Installed explicit minimal systemd target and masked inherited generators/time/scheduled/network services",
      "Created locked non-root UID/GID 10001 and private workload directory",
      "Created new regular ext4 image using mke2fs -d; no mount/chroot",
      "Validated filesystem with read-only e2fsck; boot compatibility remains unverified"
    ]
  }
}
