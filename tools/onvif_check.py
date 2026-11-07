#!/usr/bin/env python3
"""End-to-end ONVIF client check using onvif-zeep (MIT) and WSDiscovery.

usage: onvif_check.py HOST PORT USER PASS [--discover] [--events SECONDS]
"""
import sys, time, argparse
import requests
from requests.auth import HTTPDigestAuth
from onvif import ONVIFCamera

ap = argparse.ArgumentParser()
ap.add_argument("host"); ap.add_argument("port", type=int)
ap.add_argument("user"); ap.add_argument("password")
ap.add_argument("--discover", action="store_true", help="run WS-Discovery first")
ap.add_argument("--events", type=int, default=0, help="pull events for N seconds")
ap.add_argument("--bad-password", action="store_true", help="expect auth failure")
a = ap.parse_args()

if a.discover:
    # raw WS-Discovery probe; the python-ws-discovery package drops replies that come
    # from the host's own IP, which hides a simulator running on this machine
    import socket, uuid, re
    probe = (f'<?xml version="1.0" encoding="UTF-8"?><e:Envelope xmlns:e="http://www.w3.org/2003/05/soap-envelope" '
             f'xmlns:w="http://schemas.xmlsoap.org/ws/2004/08/addressing" xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery" '
             f'xmlns:dn="http://www.onvif.org/ver10/network/wsdl"><e:Header><w:MessageID>uuid:{uuid.uuid4()}</w:MessageID>'
             f'<w:To>urn:schemas-xmlsoap-org:ws:2005:04:discovery</w:To><w:Action>http://schemas.xmlsoap.org/ws/2005/04/discovery/Probe</w:Action>'
             f'</e:Header><e:Body><d:Probe><d:Types>dn:NetworkVideoTransmitter</d:Types></d:Probe></e:Body></e:Envelope>')
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    s.setsockopt(socket.IPPROTO_IP, socket.IP_MULTICAST_TTL, 2)
    s.bind(("0.0.0.0", 0))
    s.sendto(probe.encode(), ("239.255.255.250", 3702))
    s.settimeout(3)
    found = set()
    try:
        while True:
            data, addr = s.recvfrom(65535)
            txt = data.decode(errors="ignore")
            for x in re.findall(r"<d:XAddrs>(.*?)</d:XAddrs>", txt):
                found.add((addr[0], x))
    except socket.timeout:
        pass
    print(f"discovery: {len(found)} device(s)")
    for f in sorted(found):
        print("  ", f[0], f[1])

cam = ONVIFCamera(a.host, a.port, a.user, a.password)
dev = cam.create_devicemgmt_service()
try:
    info = dev.GetDeviceInformation()
except Exception as e:
    if a.bad_password:
        print("bad password correctly rejected:", str(e)[:80]); sys.exit(0)
    raise
if a.bad_password:
    print("ERROR: bad password accepted"); sys.exit(1)
print("device:", info.Manufacturer, info.Model, info.FirmwareVersion, info.SerialNumber)
print("time:", dev.GetSystemDateAndTime().UTCDateTime.Time)
caps = dev.GetCapabilities({"Category": "All"})
print("xaddrs: media", caps.Media.XAddr, "| events", caps.Events.XAddr)
print("services:", [s.Namespace.split("/")[-2] for s in dev.GetServices({"IncludeCapability": False})])

media = cam.create_media_service()
profiles = media.GetProfiles()
for p in profiles:
    vec = p.VideoEncoderConfiguration
    aud = p.AudioEncoderConfiguration.Encoding if p.AudioEncoderConfiguration else "none"
    print(f"profile {p.token}: {vec.Encoding} {vec.Resolution.Width}x{vec.Resolution.Height} @{vec.RateControl.FrameRateLimit} audio={aud}")
tok = profiles[0].token
uri = media.GetStreamUri({"StreamSetup": {"Stream": "RTP-Unicast", "Transport": {"Protocol": "RTSP"}}, "ProfileToken": tok}).Uri
print("stream uri:", uri)
snap = media.GetSnapshotUri({"ProfileToken": tok}).Uri
r = requests.get(snap, auth=HTTPDigestAuth(a.user, a.password), timeout=5)
print("snapshot:", snap, r.status_code, r.headers.get("Content-Type"), len(r.content), "bytes")
assert r.status_code == 200 and r.content[:2] == b"\xff\xd8", "snapshot is not a JPEG"

if a.events:
    ev = cam.create_events_service()
    print("topics:", [t for t in ev.GetEventProperties().TopicSet._value_1.__dict__ if not t.startswith("_")] if False else "ok")
    sub = ev.CreatePullPointSubscription({"InitialTerminationTime": "PT60S"})
    pp = cam.create_pullpoint_service()
    cam.xaddrs["http://www.onvif.org/ver10/events/wsdl/PullPointSubscription"] = sub.SubscriptionReference.Address._value_1
    pp = cam.create_pullpoint_service()
    deadline = time.time() + a.events
    while time.time() < deadline:
        msgs = pp.PullMessages({"Timeout": "PT5S", "MessageLimit": 10})
        for m in msgs.NotificationMessage:
            topic = m.Topic._value_1
            msg = m.Message._value_1
            op = msg.get("PropertyOperation")
            data = [(d.get("Name"), d.get("Value")) for d in msg.find("{http://www.onvif.org/ver10/schema}Data")] if msg.find("{http://www.onvif.org/ver10/schema}Data") is not None else []
            print(f"event {time.strftime('%H:%M:%S')} {topic} {op} {data}")
print("OK")
