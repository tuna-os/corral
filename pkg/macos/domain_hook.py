#!/usr/bin/env python3
"""VM-local OSX-KVM adaptations; invoked by KubeVirt's release sidecar-shim."""
import argparse
import sys
import xml.etree.ElementTree as ET

QEMU = "http://libvirt.org/schemas/domain/qemu/1.0"
ET.register_namespace("qemu", QEMU)
CPU = ("Skylake-Client-v4,kvm=on,vendor=GenuineIntel,+invtsc,"
       "vmware-cpuid-freq=on,-pcid,check")
# The boot image enables VirtualSMC.kext; KubeVirt's QEMU has no isa-applesmc.


def transform(domain):
    root = ET.fromstring(domain)
    # Preserve native CPU topology so cores do not become separate sockets.
    # The command-line override changes the model, not the socket/core layout.
    devices = root.find("devices")
    if devices is None:
        raise ValueError("domain has no devices")
    # Keep KubeVirt's absolute USB tablet. An extra relative USB mouse wins
    # QEMU's input selection and makes recovery-console clicks miss targets.
    # Remove the mouse added by older hook versions during a clean cutover.
    for item in list(devices.findall("input")):
        if item.get("type") == "mouse" and item.get("bus") == "usb":
            devices.remove(item)
    if not any(item.get("type") == "keyboard" and item.get("bus") == "usb"
               for item in devices.findall("input")):
        ET.SubElement(devices, "input", {"type": "keyboard", "bus": "usb"})
    # KubeVirt's QEMU omits VMware SVGA. Standard VGA supplies a UEFI GOP
    # framebuffer without requiring a macOS-specific accelerated video driver.
    video = devices.find("video")
    if video is None:
        video = ET.SubElement(devices, "video")
    model = video.find("model")
    if model is None:
        model = ET.SubElement(video, "model")
    model.attrib.clear()
    model.attrib.update({"type": "vga", "vram": "16384"})
    command = root.find("{" + QEMU + "}commandline")
    if command is None:
        command = ET.SubElement(root, "{" + QEMU + "}commandline")
    # Repeated hook invocation must not accumulate CPU or SMBIOS options.
    # Other QEMU options (and non-arg children) are retained in their order.
    children = list(command.findall("{" + QEMU + "}arg"))
    i = 0
    while i < len(children):
        child = children[i]
        value = child.get("value", "")
        following = children[i + 1] if i + 1 < len(children) else None
        operand = following.get("value", "") if following is not None else ""
        replace = (value == "-cpu"
                   or (value == "-device" and operand.startswith("isa-applesmc,"))
                   or (value == "-smbios" and operand == "type=2")
                   or (value == "-global" and operand.startswith(
                       "ICH9-LPC.acpi-pci-hotplug-with-bridge-support=")))
        if child.tag == "{" + QEMU + "}arg" and replace:
            command.remove(child)
            if following is not None:
                command.remove(following)
                i += 1
        i += 1
    # macOS uses native PCIe hotplug, not QEMU's ACPI bridge hotplug. This
    # property is local to this QEMU process, not a host kernel setting.
    for value in ("-cpu", CPU, "-smbios", "type=2", "-global",
                  "ICH9-LPC.acpi-pci-hotplug-with-bridge-support=off"):
        ET.SubElement(command, "{" + QEMU + "}arg", {"value": value})
    return ET.tostring(root, encoding="unicode")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--vmi", required=True)
    parser.add_argument("--domain", required=True)
    args = parser.parse_args()
    sys.stdout.write(transform(args.domain))


if __name__ == "__main__":
    main()
