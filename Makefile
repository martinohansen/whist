.PHONY: dev

dev:
	while true; do find * ! -name *.db | entr -rz go run . ; sleep 2; done
