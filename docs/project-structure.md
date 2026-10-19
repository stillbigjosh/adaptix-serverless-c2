## Project Structure

```
adaptix-serverless-c2/
  agent/
    src_beacon/              # Kharon implant (C++ source)
    src_loader/              # Loader wrappers (EXE, DLL, SVC)
  deploy/
    aws/
      main.tf                # Lambda + DynamoDB + IAM + KMS
      variables.tf           # Terraform variables
      outputs.tf             # Output Lambda URL and table ARNs
      lambda/
        main.go              # Lambda relay handler
  listener/
    pl_main.go               # AdaptixC2 plugin entry (Teamserver interface)
    pl_transport.go          # DynamoDB polling, Kharon protocol, LokyCrypt
    ax_config.axs            # Listener creation UI
    config.yaml              # Plugin metadata
  kharon/
    agent_kharon/            # AXS commands + config for Kharon agent extender
    listener_kharon_http/    # AXS + config for Kharon HTTP listener extender
  patches/                   # AdaptixC2 source patches
  profiles/
    lambda_default.json      # Malleable profile for Lambda URL
  scripts/
    install.sh               # Full install into AdaptixC2
    uninstall.sh             # Reverse patches + cleanup
    build_relay.sh           # Cross-compile Lambda binary
    build_agent.sh           # Build Kharon agent shellcode
    build_objects.sh         # Build beacon objects
    deploy_infra.sh          # Terraform init + apply
```
