import uasyncio as asyncio
from machine import WDT, Pin
import network
import utime
import json
import binascii
import aiorepl

WIEGAND_D0_PIN = 14
WIEGAND_D1_PIN = 27
DOOR_PIN = 25
HTTP_TIMEOUT_SECONDS = 2
POLLING_INTERVAL_SECONDS = 10
CACHE_FILE = "conwaystate.json"

with open("network.conf", "r") as f:
    NETWORK_CFG = json.load(f)


def write_json(fname, data):
    s = json.dumps(data)
    crc = binascii.crc32(s.encode()) & 0xFFFFFFFF
    obj = {"_crc32": crc, "data": data}
    with open(fname, "w") as f:
        f.write(json.dumps(obj))


def read_json(fname):
    with open(fname) as f:
        obj = json.load(f)
    s = json.dumps(obj["data"])
    crc = binascii.crc32(s.encode()) & 0xFFFFFFFF
    if crc != obj.get("_crc32"):
        raise ValueError("CRC mismatch")
    return obj["data"]


async def wifi_loop():
    net = network.WLAN(network.WLAN.IF_STA)
    net.active(True)

    while True:
        if not net.isconnected():
            print("connecting to wifi...")
            net.connect(NETWORK_CFG["ssid"], NETWORK_CFG["password"])

            for _ in range(20):
                if net.isconnected():
                    print("wifi connected:", net.ifconfig())
                    break
                await asyncio.sleep(0.5)

            if not net.isconnected():
                print("wifi connection failed, retrying in 5s...")
                net.active(False)
                await asyncio.sleep(5)
                net.active(True)

        await asyncio.sleep(3)


class ConwayClient:
    def __init__(self):
        self._etag = ""
        self._fobs = []
        self._events = []
        self._warm_cache()

    def check_fob(self, fob: int) -> bool:
        return fob in self._fobs

    def push_event(self, fob: int, allowed: bool):
        self._events.append({"fob": fob, "allowed": allowed})
        if len(self._events) > 20:
            self._events.pop(0)

    async def run_polling_loop(self):
        while True:
            await asyncio.sleep(POLLING_INTERVAL_SECONDS)
            await self.sync()

    async def sync(self):
        try:
            await self._roundtrip()
        except Exception as e:
            print(f"error while attempting to sync with Conway: {e}")

    async def _roundtrip(self):
        # Build the request
        body = json.dumps(self._events)
        req_headers = {
            "Host": NETWORK_CFG["conwayHost"],
            "Content-Type": "application/json",
            "Content-Length": str(len(body)),
            "If-None-Match": self._etag,
            "Connection": "close",
        }
        header_str = "".join(f"{k}: {v}\r\n" for k, v in req_headers.items())
        request = f"POST /api/fobs HTTP/1.1\r\n{header_str}\r\n{body}"

        # Roundtrip to the server
        reader, writer = await asyncio.open_connection(NETWORK_CFG["conwayHost"], NETWORK_CFG["conwayPort"])
        writer.write(request.encode())
        await writer.drain()
        response_data = await asyncio.wait_for(reader.read(-1), HTTP_TIMEOUT_SECONDS)
        writer.close()
        await writer.wait_closed()

        # Parse the response
        header_blob, _, body_blob = response_data.partition(b"\r\n\r\n")
        header_lines = header_blob.decode().split("\r\n")
        _, status, _ = header_lines[0].split(" ", 2)
        status = int(status)

        if status == 304:
            self._events.clear()
            return

        if status != 200:
            raise Exception(f"Unexpected server response status: {status}")

        # Find the etag in the response headers
        for line in header_lines[1:]:
            if ":" in line:
                k, v = line.split(":", 1)
                if k == "Etag":
                    self._etag = v

        # Cache the response
        self._fobs = json.loads(body_blob.decode())
        self._events.clear()
        await self._flush_cache()
        print("sync'd with conway server")

    def _warm_cache(self):
        try:
            data = read_json(CACHE_FILE)
            self._fobs = data.get("fobs", [])
            self._etag = data.get("etag", "")
            print(f"loaded {len(self._fobs)} fobs from flash")
        except Exception as e:
            print(f"unable to load filesystem cache: {e}")

    async def _flush_cache(self):
        try:
            data = {"etag": self._etag, "fobs": self._fobs}
            write_json(CACHE_FILE, data)
        except Exception as e:
            print(f"unable to write filesystem cache: {e}")


class Wiegand:
    CARD_MASK = (1 << 16) - 1  # bits 0-15: card ID (16 bits)
    FACILITY_MASK = 0xFF << 16  # bits 16-23: facility code (8 bits)
    DEBOUNCE_US = 600
    END_OF_TX_US = 4000

    def __init__(self, pin0, pin1, callback, debug=False):
        self.callback = callback
        self.debug = debug
        self.last_card = None
        self.bit_buffer = []
        self.bit_times = []  # timestamps for each bit (for debug)
        self.last_bit_time = None
        self.last_d0_time = 0
        self.last_d1_time = 0
        self.cards_read = 0
        self.debounce_rejects = 0
        self.noise_rejects = 0
        self.first_bit_time = None

        self.pin0 = Pin(pin0, Pin.IN, Pin.PULL_UP)
        self.pin1 = Pin(pin1, Pin.IN, Pin.PULL_UP)
        self.pin0.irq(trigger=Pin.IRQ_FALLING, handler=lambda *_: self._on_pin(0))
        self.pin1.irq(trigger=Pin.IRQ_FALLING, handler=lambda *_: self._on_pin(1))

    def _on_pin(self, bit_value):
        now = utime.ticks_us()
        if bit_value == 0:
            if utime.ticks_diff(now, self.last_d0_time) < Wiegand.DEBOUNCE_US:
                self.debounce_rejects += 1
                return
            self.last_d0_time = now
        else:
            if utime.ticks_diff(now, self.last_d1_time) < Wiegand.DEBOUNCE_US:
                self.debounce_rejects += 1
                return
            self.last_d1_time = now

        if self.first_bit_time is None:
            self.first_bit_time = now
        self.last_bit_time = now
        self.bit_buffer.append(bit_value)
        if self.debug:
            self.bit_times.append(now)

    async def run(self):
        while True:
            await asyncio.sleep_ms(5)
            now = utime.ticks_us()
            if self.last_bit_time and self.bit_buffer and utime.ticks_diff(now, self.last_bit_time) >= Wiegand.END_OF_TX_US:
                bit_count = len(self.bit_buffer)
                bits_str = "".join(map(str, self.bit_buffer))
                value = int(bits_str, 2)

                # Debug: compute timing stats
                if self.debug and self.bit_times:
                    self._log_timing_debug(bit_count, bits_str)

                if bit_count not in (26, 34):
                    print(f"wiegand: bad bit count {bit_count}, bits={bits_str}")
                    self.noise_rejects += 1
                    self._reset()
                    continue

                parity_ok, parity_detail = self._check_parity_debug(value, bit_count)
                if not parity_ok:
                    print(f"wiegand: parity fail ({bit_count}-bit) {parity_detail}")
                    print(f"  bits={bits_str}")
                    self.noise_rejects += 1
                    self._reset()
                    continue

                self.last_card = value
                self.cards_read += 1

                # Extract data payload (strip parity bits)
                if bit_count == 34:
                    raw_data = (value >> 1) & 0xFFFFFFFF
                else:  # 26-bit
                    raw_data = (value >> 1) & 0xFFFFFF
                await self.callback(raw_data)
                self._reset()

    def _log_timing_debug(self, bit_count, bits_str):
        """Log detailed timing info to help diagnose noise issues."""
        intervals = []
        for i in range(1, len(self.bit_times)):
            delta = utime.ticks_diff(self.bit_times[i], self.bit_times[i - 1])
            intervals.append(delta)

        if intervals:
            min_int = min(intervals)
            max_int = max(intervals)
            avg_int = sum(intervals) // len(intervals)
            total_time = utime.ticks_diff(self.bit_times[-1], self.bit_times[0])

            # Find anomalies (intervals that differ significantly from average)
            anomalies = [(i, intervals[i]) for i in range(len(intervals)) if intervals[i] < avg_int // 2 or intervals[i] > avg_int * 2]

            print(f"wiegand debug: {bit_count} bits in {total_time}us")
            print(f"  intervals: min={min_int}us avg={avg_int}us max={max_int}us")
            if anomalies:
                print(f"  anomalies: {anomalies[:5]}")  # first 5 anomalies
            print(f"  debounce_rejects={self.debounce_rejects} noise_rejects={self.noise_rejects}")

    def _check_parity_debug(self, value, bit_count):
        """Check parity and return (ok, detail_string) for debugging."""
        if bit_count == 34:
            leading, trailing = (value >> 33) & 1, value & 1
            data = (value >> 1) & 0xFFFFFFFF
            high_ones = bin(data >> 16).count("1")
            low_ones = bin(data & 0xFFFF).count("1")
            even_ok = (high_ones % 2) == leading
            odd_ok = (low_ones % 2) != trailing
            detail = f"lead={leading} trail={trailing} high_ones={high_ones} low_ones={low_ones} even_ok={even_ok} odd_ok={odd_ok}"
            return (even_ok and odd_ok), detail
        elif bit_count == 26:
            leading, trailing = (value >> 25) & 1, value & 1
            data = (value >> 1) & 0xFFFFFF
            high_ones = bin(data >> 12).count("1")
            low_ones = bin(data & 0xFFF).count("1")
            even_ok = (high_ones % 2) == leading
            odd_ok = (low_ones % 2) != trailing
            detail = f"lead={leading} trail={trailing} high_ones={high_ones} low_ones={low_ones} even_ok={even_ok} odd_ok={odd_ok}"
            return (even_ok and odd_ok), detail
        return False, "unknown format"

    def _check_parity(self, value, bit_count):
        if bit_count == 34:
            # 34-bit: parity over 16-bit halves of 32-bit data
            leading, trailing = (value >> 33) & 1, value & 1
            data = (value >> 1) & 0xFFFFFFFF
            even_ok = (bin(data >> 16).count("1") % 2) == leading
            odd_ok = (bin(data & 0xFFFF).count("1") % 2) != trailing
            return even_ok and odd_ok
        elif bit_count == 26:
            # 26-bit: parity over 12-bit halves of 24-bit data
            leading, trailing = (value >> 25) & 1, value & 1
            data = (value >> 1) & 0xFFFFFF
            even_ok = (bin(data >> 12).count("1") % 2) == leading
            odd_ok = (bin(data & 0xFFF).count("1") % 2) != trailing
            return even_ok and odd_ok
        return False

    def _reset(self):
        self.bit_buffer.clear()
        self.bit_times.clear()
        self.last_bit_time = None
        self.first_bit_time = None

    def stats(self):
        """Return debug stats for REPL inspection."""
        return {
            "cards_read": self.cards_read,
            "debounce_rejects": self.debounce_rejects,
            "noise_rejects": self.noise_rejects,
            "last_card": self.last_card,
            "debug": self.debug,
        }


class Server:
    def __init__(self, mainloop):
        self._main = mainloop

    async def run(self):
        await asyncio.start_server(lambda r, w: self._accept(r, w), "0.0.0.0", 80)

    async def _accept(self, reader, writer):
        try:
            request_line = await reader.readline()
            if not request_line:
                await writer.aclose()
                return

            # Read the request
            method, path, _ = request_line.decode().split()
            while True:
                line = await reader.readline()
                if line == b"\r\n" or not line:
                    break

            etag = self._main.conway._etag or "(none)"
            response_body = f"""
                <p>Cache ETag: {etag}</p>
                <form action="/unlock" method="post">
                    <button type="submit">Unlock</button>
                </form>
            """
            if method == "POST" and path == "/unlock":
                await self._main.open_door()
                response_body = "<p>Door unlocked!</p>"

            response = (
                "HTTP/1.1 200 OK\r\n"
                "Content-Type: text/html\r\n"
                f"Content-Length: {len(response_body)}\r\n"
                "Connection: close\r\n"
                "\r\n"
                f"{response_body}"
            )
            await writer.awrite(response)

        except Exception as e:
            print("HTTP server error:", e)
        finally:
            await writer.aclose()


class MainLoop:
    def __init__(self, conway):
        self.conway = conway
        self.door = Pin(DOOR_PIN, Pin.OUT)
        self._failed_attempts = 0
        self._backoff_until = 0

    async def on_card(self, raw_wiegand):
        now = utime.ticks_ms()
        if now < self._backoff_until:
            print("ignoring card read due to backoff")
            return

        # Decode "traditional" RFID fobs
        card_id = raw_wiegand & Wiegand.CARD_MASK
        facility = (raw_wiegand & Wiegand.FACILITY_MASK) >> 16
        fob = int(f"{facility}{card_id}")

        # Decode NFC tags
        tag = self._reverse_bytes(raw_wiegand)
        formatted_uid = f"{tag:08X}"
        formatted_uid = ":".join(formatted_uid[i : i + 2] for i in range(0, 8, 2))

        # Auth against the cache, fill the cache on failures
        print(f"scan: fob:{fob} tag:{tag} nfchex:{formatted_uid}")
        allowed = self.conway.check_fob(fob) or self.conway.check_fob(tag)
        if not allowed:
            await self.conway.sync()
            allowed = self.conway.check_fob(fob) or self.conway.check_fob(tag)

        credential = fob if self.conway.check_fob(fob) else tag
        self.conway.push_event(credential, allowed)

        if allowed:
            print("access granted")
            self._failed_attempts = 0
            asyncio.create_task(self.open_door())
        else:
            print("access denied")
            self._failed_attempts += 1
            delay = min(8000, (1 << self._failed_attempts) * 1000)
            self._backoff_until = now + delay

    async def open_door(self):
        print("opening door")
        self.door.on()
        await asyncio.sleep(0.2)
        self.door.off()

    async def run_watchdog(self):
        wdt = WDT(timeout=30000)
        while True:
            # Just make sure the asyncio loop isn't blocked
            await asyncio.sleep(20)
            wdt.feed()

    def _reverse_bytes(self, raw_data: int) -> int:
        b0 = (raw_data >> 24) & 0xFF
        b1 = (raw_data >> 16) & 0xFF
        b2 = (raw_data >> 8) & 0xFF
        b3 = raw_data & 0xFF
        return (b3 << 24) | (b2 << 16) | (b1 << 8) | b0


def boom():
    """
    Stop the asyncio worker and remove the source code.
    This makes it easy to upload new code, etc.
    """
    import os
    import sys

    os.remove("main.py")
    sys.exit(1)


conway = ConwayClient()
main = MainLoop(conway)
wiegand = Wiegand(WIEGAND_D0_PIN, WIEGAND_D1_PIN, main.on_card, debug=True)
svr = Server(main)


async def aio_main():
    await asyncio.gather(
        wifi_loop(),
        conway.run_polling_loop(),
        svr.run(),
        wiegand.run(),
        main.run_watchdog(),
        aiorepl.task(),
    )


asyncio.run(aio_main())
