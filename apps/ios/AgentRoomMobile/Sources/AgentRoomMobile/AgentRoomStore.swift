import Foundation
import SwiftUI
#if os(iOS)
import UIKit
#elseif os(macOS)
import AppKit
#endif

@MainActor
public final class AgentRoomStore: ObservableObject {
    public static let defaultRelayBase = "http://127.0.0.1:8080/"
    private static let recentRoomsKey = "agent-room.recent-rooms"
    private static let skipLoginKey = "agent-room.skip-login"
    private static let stickyTargetPrefix = "agent-room.sticky-target."

    @Published public private(set) var me = AgentRoomMe(authenticated: false, authEnabled: false, loginURL: nil, authProvider: nil, user: nil, isAdmin: nil)
    @Published public private(set) var room: AgentRoom?
    @Published public private(set) var messages: [AgentRoomMessage] = []
    @Published public private(set) var participants: [AgentRoomParticipant] = []
    @Published public private(set) var recentRooms: [AgentRoomSummary] = []
    @Published public private(set) var stickyTarget: String?
    @Published public private(set) var isLoadingAccount = false
    @Published public private(set) var isConnected = false
    @Published public private(set) var errorMessage: String?

    @Published public var didSkipLogin: Bool
    @Published public var roomDraft = ""
    @Published public var composerText = ""

    private var api: AgentRoomAPI?
    private var socket: AgentRoomWebSocket?
    private var socketTask: Task<Void, Never>?
    private var reconnectTask: Task<Void, Never>?
    private var connectionGeneration = 0
    private let identity: AgentRoomClientIdentity

    public init(identity: AgentRoomClientIdentity = .mobileDefault()) {
        self.identity = identity
        didSkipLogin = UserDefaults.standard.bool(forKey: Self.skipLoginKey)
        recentRooms = Self.loadRecentRooms()
    }

    public var needsLogin: Bool {
        !me.authenticated && !didSkipLogin
    }

    public var accountLabel: String {
        if let login = me.user?.login, !login.isEmpty {
            return "@\(login)"
        }
        return identity.principalLabel
    }

    public var authProviderLabel: String {
        me.authProvider ?? "github"
    }

    public var agentParticipants: [AgentRoomParticipant] {
        participants.filter { $0.kind == .agent }
    }

    public var stickyTargetLabel: String {
        guard let stickyTarget, !stickyTarget.isEmpty else {
            return "broadcast"
        }
        return "@\(stickyTarget)"
    }

    public var shortStickyTargetLabel: String {
        guard let stickyTarget, !stickyTarget.isEmpty else {
            return "broadcast"
        }
        return "@\(Self.shortTargetID(stickyTarget))"
    }

    public var loginURL: URL {
        let base = URL(string: Self.defaultRelayBase)!
        if let value = me.loginURL, let url = URL(string: value, relativeTo: base) {
            return url
        }
        return URL(string: "/auth/github/login?state=/", relativeTo: base)!
    }

    public func refreshAccount() {
        Task {
            await loadAccount()
        }
    }

    public func signInWithGitHub() {
        open(loginURL)
    }

    public func continueAsGuest() {
        didSkipLogin = true
        UserDefaults.standard.set(true, forKey: Self.skipLoginKey)
    }

    public func signOutLocally() {
        me = AgentRoomMe(authenticated: false, authEnabled: me.authEnabled, loginURL: me.loginURL, authProvider: me.authProvider, user: nil, isAdmin: nil)
        didSkipLogin = false
        UserDefaults.standard.set(false, forKey: Self.skipLoginKey)
    }

    public func createRoom() {
        Task {
            do {
                let api = try currentAPI()
                let created = try await api.createRoom()
                roomDraft = created.roomID
                try await join(roomID: created.roomID)
            } catch {
                show(error)
            }
        }
    }

    public func joinDraftRoom() {
        let value = roomDraft.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !value.isEmpty else { return }
        openRoom(id: value)
    }

    public func openRoom(id: String) {
        let value = id.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !value.isEmpty else { return }
        Task {
            do {
                try await join(roomID: value)
            } catch {
                show(error)
            }
        }
    }

    public func sendComposerMessage() {
        let content = composerText.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !content.isEmpty, let roomID = room?.roomID else { return }
        let explicitTarget = Self.explicitMentionTarget(in: content)
        let targetID: String?
        switch explicitTarget {
        case .target(let id):
            targetID = id
            setStickyTarget(id)
        case .broadcast:
            targetID = nil
            setStickyTarget(nil)
        case .none:
            targetID = stickyTarget
        }
        let shouldRequestReply = targetID?.isEmpty == false
        composerText = ""
        Task {
            do {
                let message = AgentRoomOutgoingMessage(
                    roomID: roomID,
                    senderID: identity.principalID,
                    targetID: targetID,
                    content: content,
                    replyRequested: shouldRequestReply,
                    turnBudget: shouldRequestReply ? 1 : 0,
                    metadata: ["source": "ios", "connection_id": identity.connectionID]
                )
                if !isConnected {
                    try await restoreRoom(roomID: roomID)
                }
                try await socket?.send(message)
            } catch {
                scheduleReconnect(roomID: roomID)
                show(error)
            }
        }
    }

    public func leaveRoom() {
        reconnectTask?.cancel()
        socketTask?.cancel()
        socket?.disconnect()
        reconnectTask = nil
        socketTask = nil
        socket = nil
        connectionGeneration += 1
        room = nil
        messages = []
        participants = []
        isConnected = false
        stickyTarget = nil
    }

    public func refreshParticipants() {
        guard let roomID = room?.roomID else { return }
        Task {
            do {
                participants = try await currentAPI().participants(roomID: roomID)
            } catch {
                show(error)
            }
        }
    }

    public func recoverConnectionIfNeeded() {
        guard let roomID = room?.roomID else { return }
        if !isConnected {
            scheduleReconnect(roomID: roomID, immediate: true)
        }
    }

    public func dismissError() {
        errorMessage = nil
    }

    public func setStickyTarget(_ targetID: String?) {
        guard let roomID = room?.roomID else { return }
        let clean = targetID?.trimmingCharacters(in: .whitespacesAndNewlines)
        let value = clean?.isEmpty == false ? clean : nil
        stickyTarget = value
        UserDefaults.standard.set(value ?? "", forKey: Self.stickyTargetKey(roomID: roomID))
    }

    public func insertMention(_ targetID: String) {
        let token = "@\(targetID) "
        if composerText.isEmpty || composerText.hasSuffix(" ") || composerText.hasSuffix("\n") {
            composerText += token
        } else {
            composerText += " \(token)"
        }
        setStickyTarget(targetID)
    }

    nonisolated public static func extractedMentionTarget(from content: String) -> String? {
        switch explicitMentionTarget(in: content) {
        case .target(let id):
            return id
        case .broadcast, .none:
            return nil
        }
    }

    private func loadAccount() async {
        isLoadingAccount = true
        defer { isLoadingAccount = false }

        do {
            me = try await currentAPI().me()
            if me.authenticated {
                didSkipLogin = false
                UserDefaults.standard.set(false, forKey: Self.skipLoginKey)
            }
        } catch {
            me = AgentRoomMe(authenticated: false, authEnabled: false, loginURL: nil, authProvider: nil, user: nil, isAdmin: nil)
        }
    }

    private func join(roomID: String) async throws {
        let api = try currentAPI()
        let loadedRoom = try await api.room(id: roomID)
        let history = try await api.messages(roomID: roomID)
        let currentParticipants = try await api.participants(roomID: roomID)

        room = loadedRoom
        messages = mergedMessages(history)
        participants = currentParticipants
        loadStickyTarget(roomID: loadedRoom.roomID, participants: currentParticipants)
        remember(room: loadedRoom)
        connectSocket(api: api, roomID: roomID)
    }

    private func connectSocket(api: AgentRoomAPI, roomID: String, preserveReconnectTask: Bool = false) {
        socketTask?.cancel()
        socket?.disconnect()
        if !preserveReconnectTask {
            clearReconnectTask()
        }
        connectionGeneration += 1
        let generation = connectionGeneration

        let nextSocket = AgentRoomWebSocket(url: api.webSocketURL(roomID: roomID, identity: identity))
        socket = nextSocket
        isConnected = true

        socketTask = Task {
            do {
                for try await message in nextSocket.messages() {
                    append(message)
                }
                handleSocketDisconnect(roomID: roomID, generation: generation)
            } catch {
                handleSocketDisconnect(roomID: roomID, generation: generation)
            }
        }
    }

    private func handleSocketDisconnect(roomID: String, generation: Int) {
        guard connectionGeneration == generation, room?.roomID == roomID else { return }
        isConnected = false
        scheduleReconnect(roomID: roomID)
    }

    private func scheduleReconnect(roomID: String, immediate: Bool = false) {
        guard room?.roomID == roomID else { return }
        if reconnectTask != nil { return }

        reconnectTask = Task { [weak self] in
            guard let self else { return }
            let delays: [UInt64] = immediate ? [0, 700, 1_500, 3_000, 5_000, 8_000, 13_000] : [700, 1_500, 3_000, 5_000, 8_000, 13_000]
            for delay in delays {
                if Task.isCancelled { return }
                if delay > 0 {
                    try? await Task.sleep(nanoseconds: delay * 1_000_000)
                }
                if Task.isCancelled { return }
                guard self.room?.roomID == roomID else { return }
                do {
                    try await self.restoreRoom(roomID: roomID)
                    self.reconnectTask = nil
                    return
                } catch {
                    self.isConnected = false
                }
            }
            self.reconnectTask = nil
        }
    }

    private func restoreRoom(roomID: String) async throws {
        let api = try currentAPI()
        let loadedRoom = try await api.room(id: roomID)
        let history = try await api.messages(roomID: roomID)
        let currentParticipants = try await api.participants(roomID: roomID)

        guard room?.roomID == roomID else { return }
        room = loadedRoom
        messages = mergedMessages(history)
        participants = currentParticipants
        loadStickyTarget(roomID: loadedRoom.roomID, participants: currentParticipants)
        remember(room: loadedRoom)
        connectSocket(api: api, roomID: roomID, preserveReconnectTask: true)
    }

    private func append(_ message: AgentRoomMessage) {
        if messages.contains(where: { $0.id == message.id }) {
            return
        }
        messages.append(message)
        if messages.count > 300 {
            messages.removeFirst(messages.count - 300)
        }
    }

    private func mergedMessages(_ history: [AgentRoomMessage]) -> [AgentRoomMessage] {
        var seen = Set<String>()
        var merged: [AgentRoomMessage] = []
        for message in history + messages {
            guard seen.insert(message.id).inserted else { continue }
            merged.append(message)
        }
        merged.sort { lhs, rhs in
            if lhs.createdAt == rhs.createdAt {
                return lhs.id < rhs.id
            }
            return lhs.createdAt < rhs.createdAt
        }
        if merged.count > 300 {
            merged.removeFirst(merged.count - 300)
        }
        return merged
    }

    private func clearReconnectTask() {
        guard let task = reconnectTask else { return }
        if !task.isCancelled {
            task.cancel()
        }
        reconnectTask = nil
    }

    private func currentAPI() throws -> AgentRoomAPI {
        guard let url = URL(string: Self.defaultRelayBase), url.scheme != nil, url.host != nil else {
            throw URLError(.badURL)
        }
        let next = AgentRoomAPI(relayBaseURL: url)
        api = next
        return next
    }

    private func show(_ error: Error) {
        errorMessage = error.localizedDescription
    }

    private func open(_ url: URL) {
        #if os(iOS)
        UIApplication.shared.open(url)
        #elseif os(macOS)
        NSWorkspace.shared.open(url)
        #endif
    }

    private func remember(room: AgentRoom) {
        let summary = AgentRoomSummary(room: room)
        recentRooms.removeAll { $0.id == summary.id }
        recentRooms.insert(summary, at: 0)
        if recentRooms.count > 12 {
            recentRooms.removeLast(recentRooms.count - 12)
        }
        saveRecentRooms()
    }

    private func saveRecentRooms() {
        guard let data = try? JSONEncoder().encode(recentRooms) else { return }
        UserDefaults.standard.set(data, forKey: Self.recentRoomsKey)
    }

    private static func loadRecentRooms() -> [AgentRoomSummary] {
        guard
            let data = UserDefaults.standard.data(forKey: recentRoomsKey),
            let value = try? JSONDecoder().decode([AgentRoomSummary].self, from: data)
        else {
            return []
        }
        return value
    }

    private func loadStickyTarget(roomID: String, participants: [AgentRoomParticipant]) {
        let key = Self.stickyTargetKey(roomID: roomID)
        if UserDefaults.standard.object(forKey: key) != nil {
            let stored = UserDefaults.standard.string(forKey: key) ?? ""
            stickyTarget = stored.isEmpty ? nil : stored
            return
        }

        let agents = participants.filter { $0.kind == .agent }
        stickyTarget = agents.count == 1 ? agents[0].id : nil
    }

    private static func stickyTargetKey(roomID: String) -> String {
        stickyTargetPrefix + roomID
    }

    private static func shortTargetID(_ targetID: String) -> String {
        guard targetID.count > 18 else { return targetID }
        return "\(targetID.prefix(10))...\(targetID.suffix(6))"
    }

    nonisolated private static func explicitMentionTarget(in content: String) -> ExplicitMentionTarget {
        guard let regex = try? NSRegularExpression(pattern: "(?:^|\\s)@([A-Za-z0-9_-]{1,64})(?:\\s|$|[,:;.!?])") else {
            return .none
        }
        let range = NSRange(content.startIndex..<content.endIndex, in: content)
        guard
            let match = regex.firstMatch(in: content, range: range),
            match.numberOfRanges >= 2,
            let targetRange = Range(match.range(at: 1), in: content)
        else {
            return .none
        }
        let target = String(content[targetRange]).trimmingCharacters(in: .whitespacesAndNewlines)
        if target.caseInsensitiveCompare("all") == .orderedSame || target.caseInsensitiveCompare("room") == .orderedSame {
            return .broadcast
        }
        return target.isEmpty ? .none : .target(target)
    }
}

private enum ExplicitMentionTarget {
    case none
    case broadcast
    case target(String)
}

public struct AgentRoomSummary: Codable, Identifiable, Equatable, Sendable {
    public let id: String
    public let title: String?
    public let createdAt: Date
    public let lastOpenedAt: Date

    public init(room: AgentRoom, lastOpenedAt: Date = Date()) {
        id = room.roomID
        title = room.title
        createdAt = room.createdAt
        self.lastOpenedAt = lastOpenedAt
    }
}

extension AgentRoomClientIdentity {
    public static func mobileDefault() -> AgentRoomClientIdentity {
        let id = UserDefaults.standard.string(forKey: "agent-room.mobile-id") ?? {
            let value = "ios-\(UUID().uuidString.prefix(8).lowercased())"
            UserDefaults.standard.set(value, forKey: "agent-room.mobile-id")
            return value
        }()
        return AgentRoomClientIdentity(
            connectionID: id,
            connectionLabel: "iPhone",
            principalID: id,
            principalLabel: "iPhone"
        )
    }
}
