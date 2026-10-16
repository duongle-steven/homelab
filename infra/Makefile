.POSIX:
.PHONY: check

# Offline: HCL is valid and formatted (part of the basic checks)
check:
	terragrunt hcl validate
	tofu fmt -check -recursive .
	terragrunt hcl fmt --check
