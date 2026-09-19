. as $manifest |
.api_version == "epoch-workload/v1" and
.uid == 10001 and .gid == 10001 and
.working_dir == "/var/lib/epoch-workload" and
([.actions[].argv[0], .services[].argv[0]] | unique) == [$executable]
