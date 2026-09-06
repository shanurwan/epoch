{
  api_version: "epoch-preparation-inputs/v1",
  source_identity: $source,
  checksum_origin: "locally-observed",
  userspace: {
    kind: $kind,
    path: $path,
    sha256: (if $hash == "" then null else $hash end),
    size_bytes: (if $size == 0 then null else $size end),
    marker_sha256: (if $marker == "" then null else $marker end)
  },
  kernel: {path: $kp, sha256: $kh, size_bytes: $ks},
  guest_agent: {path: $ap, sha256: $ah, size_bytes: $agent_size},
  clock_probe: {path: $pp, sha256: $ph, size_bytes: $ps},
  limitations: [
    "Observed hashes identify local content, not published checksums or signatures",
    "A prepared-directory marker digest is not a digest of directory contents",
    "Extraction/filesystem checks do not verify guest boot compatibility"
  ]
}
