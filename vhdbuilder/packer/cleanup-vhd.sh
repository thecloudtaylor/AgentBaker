#!/bin/bash -eux

# Cleanup packer SSH key and machine ID generated for this boot
rm -f /root/.ssh/authorized_keys
rm -f /home/packer/.ssh/authorized_keys
rm -f /var/log/cloud-init.log /var/log/cloud-init-output.log 
rm -f /etc/machine-id
touch /etc/machine-id
chmod 644 /etc/machine-id