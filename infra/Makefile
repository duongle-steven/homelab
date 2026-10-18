.POSIX:
.PHONY: check

# Offline: HCL is valid and formatted (part of the basic checks); production's credentials are placeholders, no cf login
check:
	HOMELAB_CLOUDFLARE_OFFLINE=1 terragrunt hcl validate
	tofu fmt -check -recursive .
	terragrunt hcl fmt --check
