FROM hub.ynhtapp.com/docker.io/alpine:3.17.2
MAINTAINER HeYuJie

ENV TZ=Asia/Shanghai

COPY bin/go_build_linux_linux /filesystem-provisioner
RUN chmod +x /filesystem-provisioner

ENTRYPOINT ["/filesystem-provisioner"]