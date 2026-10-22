"""pysmb opens every session with an SMB1 negotiate that offers SMB2 among
its dialects. A server with SMB1 switched off — Samba's `server min protocol =
SMB2`, most NAS boxes, a hardened Windows — drops that packet, and the client
concludes the server "supports no dialect". This connection speaks SMB2 from
the first byte: the NEGOTIATE request of [MS-SMB2] 2.2.3 with dialects 2.0.2
and 2.1 (the two pysmb implements), MessageId 0 as the sequence requires. The
reply lands in pysmb's own SMB2 path, untouched.

One more thing 2.1 brings: a request for more than 64 KiB costs several
credits, and pysmb never charges any ("CreditCharge too low, given 1, needed
128" is how Samba refuses an 8 MiB directory read). So the sizes the server
offers are brought down to one credit's worth — a file is still read whole,
in 64 KiB blocks, which is what a 2.0.2 server offers anyway."""

from __future__ import annotations

import os
import struct

from smb.SMBConnection import SMBConnection
from smb.smb2_constants import SMB2_COM_NEGOTIATE, SMB2_DIALECT_2, SMB2_DIALECT_21, SMB2_NEGOTIATE_SIGNING_ENABLED
from smb.smb2_structs import SMB2Message, Structure


class Smb2NegotiateRequest(Structure):
    """[MS-SMB2] 2.2.3 — StructureSize, DialectCount, SecurityMode, Reserved,
    Capabilities, ClientGuid, ClientStartTime, Dialects."""

    DIALECTS = (SMB2_DIALECT_2, SMB2_DIALECT_21)

    def initMessage(self, message):
        Structure.initMessage(self, message)
        message.command = SMB2_COM_NEGOTIATE

    def prepare(self, message):
        message.data = struct.pack("<HHHHI16sQ", 36, len(self.DIALECTS), SMB2_NEGOTIATE_SIGNING_ENABLED, 0, 0,
                                   os.urandom(16), 0) + struct.pack("<%dH" % len(self.DIALECTS), *self.DIALECTS)


ONE_CREDIT = 65536


class Smb2Connection(SMBConnection):
    def _updateServerInfo_SMB2(self, payload):
        SMBConnection._updateServerInfo_SMB2(self, payload)
        self.max_read_size = min(self.max_read_size, ONE_CREDIT)
        self.max_write_size = min(self.max_write_size, ONE_CREDIT)
        self.max_transact_size = min(self.max_transact_size, ONE_CREDIT)

    def onNMBSessionOK(self):
        self.is_using_smb2 = True
        self._setupSMB2Methods()
        self.smb_message = SMB2Message()
        self.mid = 0
        request = SMB2Message(Smb2NegotiateRequest())
        request.raw_data = request.encode()
        self.sendNMBMessage(request.raw_data)
