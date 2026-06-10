import XCTest
@testable import AgentRoomMobile

final class AgentRoomModelTests: XCTestCase {
    func testDecodesRelayMessageEnvelope() throws {
        let json = """
        {
          "id": "msg_1",
          "room_id": "demo",
          "type": "chat",
          "sender_id": "operator",
          "sender_kind": "user",
          "content": "hello from iPhone",
          "reply_requested": true,
          "turn_budget": 1,
          "created_at": "2026-06-05T10:11:12.123Z",
          "metadata": {
            "source": "ios"
          }
        }
        """.data(using: .utf8)!

        let message = try AgentRoomCoding.decoder.decode(AgentRoomMessage.self, from: json)

        XCTAssertEqual(message.id, "msg_1")
        XCTAssertEqual(message.roomID, "demo")
        XCTAssertEqual(message.type, .chat)
        XCTAssertEqual(message.senderKind, .user)
        XCTAssertEqual(message.metadata?["source"], "ios")
    }

    func testEncodesOutgoingMessageUsingRelayKeys() throws {
        let message = AgentRoomOutgoingMessage(
            roomID: "demo",
            senderID: "ios-test",
            content: "ping",
            metadata: ["source": "ios"]
        )

        let data = try AgentRoomCoding.encoder.encode(message)
        let object = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])

        XCTAssertEqual(object["room_id"] as? String, "demo")
        XCTAssertEqual(object["sender_id"] as? String, "ios-test")
        XCTAssertEqual(object["sender_kind"] as? String, "user")
        XCTAssertEqual(object["reply_requested"] as? Bool, false)
    }

    func testEncodesTargetedMentionMessage() throws {
        let message = AgentRoomOutgoingMessage(
            roomID: "demo",
            senderID: "ios-test",
            targetID: "agent-1",
            content: "@agent-1 ping",
            replyRequested: true,
            turnBudget: 1,
            metadata: ["source": "ios"]
        )

        let data = try AgentRoomCoding.encoder.encode(message)
        let object = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])

        XCTAssertEqual(object["target_id"] as? String, "agent-1")
        XCTAssertEqual(object["reply_requested"] as? Bool, true)
        XCTAssertEqual(object["turn_budget"] as? Int, 1)
    }

    func testExtractsMentionTarget() {
        XCTAssertEqual(AgentRoomStore.extractedMentionTarget(from: "@agent-1 ping"), "agent-1")
        XCTAssertEqual(AgentRoomStore.extractedMentionTarget(from: "please ask @agent_2, thanks"), "agent_2")
        XCTAssertNil(AgentRoomStore.extractedMentionTarget(from: "broadcast @all hello"))
        XCTAssertNil(AgentRoomStore.extractedMentionTarget(from: "email@agent is not a mention"))
    }

    func testTimelineAttachesThinkingBeforeAgentReply() {
        let base = Date(timeIntervalSince1970: 1_800_000_000)
        let user = makeMessage(
            id: "user-1",
            senderID: "iphone",
            senderKind: .user,
            targetID: "agent-1",
            content: "在吗？",
            createdAt: base
        )
        let thinking = makeMessage(
            id: "trace-1",
            type: .trace,
            senderID: "agent-1",
            senderKind: .agent,
            targetID: "iphone",
            content: "thinking",
            createdAt: base.addingTimeInterval(1),
            metadata: ["phase": "thinking"]
        )
        let reply = makeMessage(
            id: "agent-1-reply",
            senderID: "agent-1",
            senderKind: .agent,
            targetID: "iphone",
            content: "我在。",
            createdAt: base.addingTimeInterval(2)
        )

        let entries = buildTimelineEntries([user, thinking, reply])

        XCTAssertEqual(entries.count, 2)
        XCTAssertEqual(entries[0].message?.id, "user-1")
        XCTAssertTrue(entries[0].traces.isEmpty)
        XCTAssertEqual(entries[1].message?.id, "agent-1-reply")
        XCTAssertEqual(entries[1].traces.map(\.id), ["trace-1"])
    }

    private func makeMessage(
        id: String,
        type: AgentRoomMessageType = .chat,
        senderID: String,
        senderKind: AgentRoomSenderKind,
        targetID: String?,
        content: String,
        createdAt: Date,
        metadata: [String: String]? = nil
    ) -> AgentRoomMessage {
        AgentRoomMessage(
            id: id,
            roomID: "demo",
            type: type,
            senderID: senderID,
            senderKind: senderKind,
            targetID: targetID,
            content: content,
            replyRequested: false,
            turnBudget: 0,
            createdAt: createdAt,
            metadata: metadata
        )
    }
}
