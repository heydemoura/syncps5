.PHONY: all toolchain deploy logs clean
all:
	tools/build.sh
toolchain:
	tools/setup-toolchain.sh
deploy: all
	tools/deploy.sh
logs:
	tools/logs.sh logs/ps5.log
clean:
	rm -rf build out
