import SwiftUI
#if os(iOS)
import UIKit
#endif

@MainActor
public struct AgentRoomRootView: View {
    @Environment(\.scenePhase) private var scenePhase
    @StateObject private var store: AgentRoomStore
    @State private var selectedTab: MobileTab = .rooms
    @State private var backSwipeOffset: CGFloat = 0
    @FocusState private var focusedField: AgentRoomFocusField?

    public init() {
        _store = StateObject(wrappedValue: AgentRoomStore())
    }

    public init(store: AgentRoomStore) {
        _store = StateObject(wrappedValue: store)
    }

    public var body: some View {
        GeometryReader { proxy in
            ZStack {
                AgentRoomPalette.canvas.ignoresSafeArea()
                DotMatrixBackground().ignoresSafeArea()

                Group {
                    if store.needsLogin {
                        loginScreen
                    } else {
                        appShell(width: proxy.size.width)
                    }
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .padding(.top, max(proxy.safeAreaInsets.top + 6, 14))
                .padding(.bottom, focusedField == .composer ? 0 : max(proxy.safeAreaInsets.bottom, 8))
            }
            .ignoresSafeArea(.container, edges: [.top, .bottom])
        }
        .preferredColorScheme(.dark)
        .task {
            store.refreshAccount()
        }
        .onChange(of: scenePhase) {
            if scenePhase == .active {
                store.recoverConnectionIfNeeded()
            }
        }
        .toolbar {
            ToolbarItemGroup(placement: .keyboard) {
                keyboardToolbar
            }
        }
        .alert("Agent Room", isPresented: Binding(
            get: { store.errorMessage != nil },
            set: { if !$0 { store.dismissError() } }
        )) {
            Button("OK", role: .cancel) {}
        } message: {
            Text(store.errorMessage ?? "")
        }
    }

    @ViewBuilder
    private var keyboardToolbar: some View {
        if focusedField == .roomID {
            Spacer()

            Button("Done") {
                focusedField = nil
            }
            .font(.system(.body, design: .monospaced).weight(.semibold))

            Button("Join") {
                focusedField = nil
                if store.needsLogin {
                    store.continueAsGuest()
                }
                store.joinDraftRoom()
            }
            .font(.system(.body, design: .monospaced).weight(.bold))
            .disabled(store.roomDraft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
        }
    }

    private var loginScreen: some View {
        VStack(spacing: 8) {
            TerminalTopBar(
                left: "agent-room",
                middle: "auth",
                right: "github W+",
                leadingIcon: nil,
                trailingIcon: "arrow.clockwise",
                leadingAction: nil
            ) {
                store.refreshAccount()
            }

            TerminalPanel(spacing: 10) {
                HStack(spacing: 8) {
                    Image(systemName: "terminal.fill")
                        .foregroundStyle(AgentRoomPalette.me)
                    VStack(alignment: .leading, spacing: 2) {
                        Text("login")
                            .terminalHeadline()
                            .foregroundStyle(AgentRoomPalette.text)
                        Text("fixed relay · 127.0.0.1:8080")
                            .terminalCaption()
                            .foregroundStyle(AgentRoomPalette.dim)
                    }
                    Spacer()
                    StatusPill(title: store.authProviderLabel, systemImage: "person.badge.key.fill", tint: AgentRoomPalette.ai)
                }

                Text("GitHub 登录用于识别房间 owner、审计身份和后续房间列表。也可以先以访客进入，只输入房间 ID。")
                    .font(.system(.footnote, design: .monospaced))
                    .foregroundStyle(AgentRoomPalette.status)
                    .lineSpacing(2)

                HStack(spacing: 8) {
                    Button {
                        store.signInWithGitHub()
                    } label: {
                        Label("GitHub", systemImage: "person.crop.circle.badge.checkmark")
                            .font(.system(.callout, design: .monospaced).weight(.bold))
                            .frame(maxWidth: .infinity)
                            .frame(height: 44)
                    }
                    .foregroundStyle(AgentRoomPalette.canvas)
                    .background(AgentRoomPalette.me, in: RoundedRectangle(cornerRadius: 6))
                    .buttonStyle(.plain)

                    Button {
                        store.continueAsGuest()
                    } label: {
                        Text("guest")
                            .font(.system(.callout, design: .monospaced).weight(.bold))
                            .frame(width: 96, height: 44)
                    }
                    .foregroundStyle(AgentRoomPalette.ai)
                    .background(AgentRoomPalette.ai.opacity(0.13), in: RoundedRectangle(cornerRadius: 6))
                    .overlay(RoundedRectangle(cornerRadius: 6).stroke(AgentRoomPalette.ai.opacity(0.44), lineWidth: 1))
                    .buttonStyle(.plain)
                }
            }

            TerminalPanel(spacing: 8) {
                MiniFeatureRow(icon: "number", title: "room id only", value: "无需输入域名")
                MiniFeatureRow(icon: "shield.checkered", title: "owner approval", value: "命令执行授权")
                MiniFeatureRow(icon: "clock.arrow.circlepath", title: "audit stream", value: "活动和历史记录")
            }

            CommandJoinPanel(
                roomDraft: $store.roomDraft,
                focus: $focusedField,
                canJoin: !store.roomDraft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty,
                join: {
                    focusedField = nil
                    store.continueAsGuest()
                    store.joinDraftRoom()
                },
                create: {
                    focusedField = nil
                    store.continueAsGuest()
                    store.createRoom()
                }
            )

            workspacePreview

            Spacer(minLength: 0)
        }
        .padding(.horizontal, 10)
        .scrollDismissesKeyboardIfAvailable()
    }

    private var workspacePreview: some View {
        TerminalPanel(spacing: 0) {
            HStack {
                Text("workspace")
                    .terminalCaption()
                    .foregroundStyle(AgentRoomPalette.status)
                Spacer()
                Text("web console mapped")
                    .terminalCaption()
                    .foregroundStyle(AgentRoomPalette.dim)
            }
            .padding(.horizontal, 10)
            .frame(height: 30)

            Divider().overlay(AgentRoomPalette.border)

            MiniFeatureRow(icon: "number", title: "rooms", value: "创建 · 加入 · 最近房间")
                .padding(.horizontal, 10)
                .frame(height: 40)
            MiniFeatureRow(icon: "waveform.path.ecg", title: "activity", value: "消息 · 命令 · trace")
                .padding(.horizontal, 10)
                .frame(height: 40)
            MiniFeatureRow(icon: "cpu", title: "agents", value: "Bridge · Executor")
                .padding(.horizontal, 10)
                .frame(height: 40)
            MiniFeatureRow(icon: "gearshape", title: "settings", value: "Auth · Relay · Audit")
                .padding(.horizontal, 10)
                .frame(height: 40)
        }
    }

    private func appShell(width: CGFloat) -> some View {
        VStack(spacing: 8) {
            TerminalTopBar(
                left: "agent-room",
                middle: store.accountLabel,
                right: store.room == nil ? "ready W+" : (store.isConnected ? "live W+" : "sync W?"),
                leadingIcon: store.room == nil ? nil : "chevron.left",
                trailingIcon: selectedTab == .rooms && store.room == nil ? "plus" : "arrow.clockwise",
                leadingAction: store.room == nil ? nil : { leaveCurrentRoom() }
            ) {
                if selectedTab == .rooms && store.room == nil {
                    store.createRoom()
                } else if store.room != nil {
                    store.refreshParticipants()
                } else {
                    store.refreshAccount()
                }
            }

            Group {
                switch selectedTab {
                case .rooms:
                    if let room = store.room {
                        interactiveRoomWorkspace(room, width: width)
                    } else {
                        roomListScreen
                            .transition(.asymmetric(
                                insertion: .move(edge: .leading).combined(with: .opacity),
                                removal: .move(edge: .leading).combined(with: .opacity)
                            ))
                    }
                case .activity:
                    activityScreen
                case .agents:
                    agentsScreen
                case .settings:
                    settingsScreen
                }
            }
            .contentShape(Rectangle())
            .simultaneousGesture(TapGesture().onEnded {
                dismissKeyboard()
            })
            .animation(.easeOut(duration: 0.22), value: store.room?.roomID ?? "room-list")

            Spacer(minLength: 0)

            if focusedField != .composer {
                TerminalTabBar(selectedTab: $selectedTab)
            }
        }
        .padding(.horizontal, 10)
        .overlay(alignment: .leading) {
            if selectedTab == .rooms && store.room != nil {
                Color.clear
                    .frame(width: 24)
                    .contentShape(Rectangle())
                    .gesture(backSwipeGesture(width: width))
                    .accessibilityHidden(true)
            }
        }
        .safeAreaInset(edge: .bottom) {
            if selectedTab == .rooms && store.room != nil {
                composer
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
    }

    private func interactiveRoomWorkspace(_ room: AgentRoom, width: CGFloat) -> some View {
        let progress = min(max(backSwipeOffset / max(width, 1), 0), 1)

        return ZStack(alignment: .leading) {
            AgentRoomPalette.canvas
                .overlay(DotMatrixBackground().opacity(0.72))
                .overlay(alignment: .leading) {
                    Rectangle()
                        .fill(AgentRoomPalette.me.opacity(0.14 * progress))
                        .frame(width: 2)
                }
                .allowsHitTesting(false)

            roomWorkspace(room)
                .offset(x: backSwipeOffset)
                .shadow(color: .black.opacity(0.34 * progress), radius: 20 * progress, x: -10 * progress, y: 0)
                .overlay(alignment: .leading) {
                    Rectangle()
                        .fill(.black.opacity(0.18 * progress))
                        .frame(width: 1)
                        .opacity(progress > 0 ? 1 : 0)
                }
        }
        .clipShape(Rectangle())
        .transition(.asymmetric(
            insertion: .move(edge: .trailing).combined(with: .opacity),
            removal: .move(edge: .trailing).combined(with: .opacity)
        ))
    }

    private func backSwipeGesture(width: CGFloat) -> some Gesture {
        DragGesture(minimumDistance: 8, coordinateSpace: .local)
            .onChanged { value in
                guard selectedTab == .rooms, store.room != nil else { return }
                guard value.translation.width > 0 else {
                    backSwipeOffset = 0
                    return
                }

                let horizontal = value.translation.width
                let vertical = abs(value.translation.height)
                guard horizontal > vertical * 1.45 else { return }

                dismissKeyboard()
                backSwipeOffset = min(horizontal, width * 0.92)
            }
            .onEnded { value in
                guard selectedTab == .rooms, store.room != nil else { return }
                let horizontal = max(value.translation.width, 0)
                let vertical = abs(value.translation.height)
                let predicted = max(value.predictedEndTranslation.width, horizontal)
                let shouldReturn = horizontal > min(width * 0.34, 150) || (predicted > min(width * 0.48, 220) && horizontal > vertical * 1.35)

                if shouldReturn {
                    withAnimation(.easeOut(duration: 0.18)) {
                        backSwipeOffset = width
                    }
                    DispatchQueue.main.asyncAfter(deadline: .now() + 0.16) {
                        leaveCurrentRoom(animated: false)
                    }
                } else {
                    withAnimation(.interactiveSpring(response: 0.26, dampingFraction: 0.86)) {
                        backSwipeOffset = 0
                    }
                }
            }
    }

    private var roomListScreen: some View {
        VStack(spacing: 8) {
            CommandJoinPanel(
                roomDraft: $store.roomDraft,
                focus: $focusedField,
                canJoin: !store.roomDraft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty,
                join: {
                    focusedField = nil
                    store.joinDraftRoom()
                },
                create: {
                    focusedField = nil
                    store.createRoom()
                }
            )

            recentRoomsSection

            TerminalPanel(spacing: 8) {
                MiniFeatureRow(icon: "link", title: "service", value: "线上 relay 已写死")
                MiniFeatureRow(icon: "person.2.fill", title: "rooms", value: "创建 / 加入 / 最近访问")
            }
        }
    }

    private var recentRoomsSection: some View {
        TerminalPanel(spacing: 0) {
            HStack(spacing: 8) {
                Text("rooms")
                    .terminalCaption()
                    .foregroundStyle(AgentRoomPalette.status)

                Text(String(format: "%02d", store.recentRooms.count))
                    .terminalCaption()
                    .foregroundStyle(AgentRoomPalette.dim)

                Spacer()

                Text("recent")
                    .terminalCaption()
                    .foregroundStyle(AgentRoomPalette.dim)
            }
            .padding(.horizontal, 10)
            .frame(height: 30)

            Divider().overlay(AgentRoomPalette.border)

            if store.recentRooms.isEmpty {
                EmptyRoomsRow {
                    store.createRoom()
                }
            } else {
                ForEach(store.recentRooms) { room in
                    Button {
                        store.openRoom(id: room.id)
                    } label: {
                        RoomListRow(room: room)
                    }
                    .buttonStyle(.plain)

                    if room.id != store.recentRooms.last?.id {
                        Divider().overlay(AgentRoomPalette.border).padding(.leading, 10)
                    }
                }
            }
        }
    }

    private func roomWorkspace(_ room: AgentRoom) -> some View {
        VStack(spacing: 8) {
            roomStatusPanel(room)
            messageList
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private func roomStatusPanel(_ room: AgentRoom) -> some View {
        TerminalPanel(spacing: 8) {
            HStack(spacing: 8) {
                Text(room.title ?? "session")
                    .terminalHeadline()
                    .foregroundStyle(AgentRoomPalette.text)
                    .lineLimit(1)

                Spacer()

                StatusPill(
                    title: store.isConnected ? "connected" : "retrying",
                    systemImage: store.isConnected ? "checkmark.circle.fill" : "bolt.horizontal.circle",
                    tint: store.isConnected ? AgentRoomPalette.me : AgentRoomPalette.warning
                )
            }

            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 6) {
                    TerminalTag(text: "target \(store.shortStickyTargetLabel)", tint: store.stickyTarget == nil ? AgentRoomPalette.status : AgentRoomPalette.me)

                    if store.participants.isEmpty {
                        TerminalTag(text: "waiting for peers", tint: AgentRoomPalette.warning)
                    } else {
                        ForEach(store.participants.prefix(10)) { participant in
                            TerminalTag(text: participant.label, tint: participant.kind.terminalColor)
                        }
                    }
                }
            }
        }
        .padding(.horizontal, 0)
    }

    private var activityScreen: some View {
        TerminalPanel(spacing: 0) {
            HStack {
                Text("activity")
                    .terminalCaption()
                    .foregroundStyle(AgentRoomPalette.status)
                Spacer()
                Text(store.room?.roomID ?? "no room")
                    .terminalCaption()
                    .foregroundStyle(AgentRoomPalette.dim)
                    .lineLimit(1)
                    .truncationMode(.middle)
            }
            .padding(.horizontal, 10)
            .frame(height: 30)

            Divider().overlay(AgentRoomPalette.border)

            if store.messages.isEmpty {
                MiniFeatureRow(icon: "waveform.path.ecg", title: "no events", value: "进入房间后显示实时审计")
                    .padding(.horizontal, 10)
                    .frame(height: 48)
            } else {
                ForEach(store.messages.suffix(8)) { message in
                    ActivityRow(message: message)
                    if message.id != store.messages.suffix(8).last?.id {
                        Divider().overlay(AgentRoomPalette.border).padding(.leading, 10)
                    }
                }
            }
        }
    }

    private var agentsScreen: some View {
        TerminalPanel(spacing: 0) {
            HStack {
                Text("agents")
                    .terminalCaption()
                    .foregroundStyle(AgentRoomPalette.status)
                Spacer()
                Text("\(store.participants.count) peers")
                    .terminalCaption()
                    .foregroundStyle(AgentRoomPalette.dim)
            }
            .padding(.horizontal, 10)
            .frame(height: 30)

            Divider().overlay(AgentRoomPalette.border)

            if store.participants.isEmpty {
                MiniFeatureRow(icon: "cpu", title: "waiting", value: "Bridge / Agent 加入后显示")
                    .padding(.horizontal, 10)
                    .frame(height: 50)
            } else {
                Button {
                    store.setStickyTarget(nil)
                } label: {
                    MentionTargetRow(
                        icon: "dot.radiowaves.left.and.right",
                        title: "broadcast",
                        subtitle: "不默认召唤 agent",
                        isSelected: store.stickyTarget == nil,
                        tint: AgentRoomPalette.status
                    )
                }
                .buttonStyle(.plain)

                Divider().overlay(AgentRoomPalette.border).padding(.leading, 10)

                ForEach(store.participants) { participant in
                    if participant.kind == .agent {
                        Button {
                            store.setStickyTarget(participant.id)
                        } label: {
                            ParticipantRow(participant: participant, isSelected: store.stickyTarget == participant.id)
                        }
                        .buttonStyle(.plain)
                    } else {
                        ParticipantRow(participant: participant, isSelected: false)
                    }
                    if participant.id != store.participants.last?.id {
                        Divider().overlay(AgentRoomPalette.border).padding(.leading, 10)
                    }
                }
            }
        }
    }

    private var settingsScreen: some View {
        VStack(spacing: 8) {
            TerminalPanel(spacing: 8) {
                HStack {
                    VStack(alignment: .leading, spacing: 2) {
                        Text(store.me.authenticated ? "signed in" : "guest mode")
                            .terminalHeadline()
                            .foregroundStyle(AgentRoomPalette.text)
                        Text(store.me.user?.login ?? "GitHub auth not linked")
                            .terminalCaption()
                            .foregroundStyle(AgentRoomPalette.dim)
                    }
                    Spacer()
                    StatusPill(
                        title: store.me.authenticated ? "verified" : "optional",
                        systemImage: store.me.authenticated ? "checkmark.seal.fill" : "person.crop.circle",
                        tint: store.me.authenticated ? AgentRoomPalette.me : AgentRoomPalette.warning
                    )
                }

                HStack(spacing: 8) {
                    Button {
                        store.signInWithGitHub()
                    } label: {
                        Text("github auth")
                            .font(.system(.caption, design: .monospaced).weight(.bold))
                            .frame(maxWidth: .infinity)
                            .frame(height: 44)
                    }
                    .foregroundStyle(AgentRoomPalette.canvas)
                    .background(AgentRoomPalette.me, in: RoundedRectangle(cornerRadius: 6))
                    .buttonStyle(.plain)

                    Button {
                        store.signOutLocally()
                    } label: {
                        Text("reset")
                            .font(.system(.caption, design: .monospaced).weight(.bold))
                            .frame(width: 78, height: 44)
                    }
                    .foregroundStyle(AgentRoomPalette.warning)
                    .background(AgentRoomPalette.warning.opacity(0.12), in: RoundedRectangle(cornerRadius: 6))
                    .overlay(RoundedRectangle(cornerRadius: 6).stroke(AgentRoomPalette.warning.opacity(0.38), lineWidth: 1))
                    .buttonStyle(.plain)
                }
            }

            TerminalPanel(spacing: 8) {
                MiniFeatureRow(icon: "server.rack", title: "relay", value: "127.0.0.1:8080")
                MiniFeatureRow(icon: "lock.fill", title: "room address", value: "只输入房间 ID")
                MiniFeatureRow(icon: "checkmark.shield.fill", title: "approval", value: "Owner / Admin 授权")
                MiniFeatureRow(icon: "doc.text.magnifyingglass", title: "audit", value: "messages · commands · traces")
            }
        }
    }

    private var messageList: some View {
        ScrollViewReader { proxy in
            let entries = buildTimelineEntries(store.messages)
            let bottomAnchorID = "timeline-bottom-anchor"
            let scrollSignature = "\(store.messages.count):\(entries.last?.id ?? "empty"):\(entries.last?.traces.count ?? 0)"

            List {
                ForEach(entries) { entry in
                    TimelineEntryRow(entry: entry)
                        .id(entry.id)
                        .listRowSeparator(.hidden)
                        .listRowBackground(Color.clear)
                        .listRowInsets(EdgeInsets(top: 3, leading: 0, bottom: 3, trailing: 0))
                }

                Color.clear
                    .frame(height: 1)
                    .id(bottomAnchorID)
                    .listRowSeparator(.hidden)
                    .listRowBackground(Color.clear)
                    .listRowInsets(EdgeInsets(top: 0, leading: 0, bottom: 0, trailing: 0))
            }
            .listStyle(.plain)
            .scrollContentBackground(.hidden)
            .scrollDismissesKeyboardIfAvailable()
            .overlay {
                if store.messages.isEmpty {
                    EmptyConversationView()
                }
            }
            .onAppear {
                scrollTimelineToBottom(proxy, bottomAnchorID: bottomAnchorID, animated: false)
            }
            .onChange(of: scrollSignature) {
                scrollTimelineToBottom(proxy, bottomAnchorID: bottomAnchorID, animated: true)
            }
            .onChange(of: focusedField) {
                if focusedField == .composer {
                    scrollTimelineToBottom(proxy, bottomAnchorID: bottomAnchorID, animated: true)
                }
            }
            .simultaneousGesture(TapGesture().onEnded {
                dismissKeyboard()
            })
        }
    }

    private var composer: some View {
        VStack(spacing: 7) {
            HStack(spacing: 8) {
                mentionTargetMenu

                TextField("请输入...", text: $store.composerText, axis: .vertical)
                    .agentRoomMessageInput()
                    .focused($focusedField, equals: .composer)
                    .lineLimit(1...4)
                    .textFieldStyle(.plain)
                    .font(.system(.body, design: .monospaced).weight(.medium))
                    .foregroundStyle(AgentRoomPalette.canvas)
                    .tint(AgentRoomPalette.me)
                    .onSubmit {
                        store.sendComposerMessage()
                    }

                if store.composerText.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                    ComposerIconButton(systemName: "mic.fill", tint: AgentRoomPalette.status) {}
                        .accessibilityLabel("Voice input")
                } else {
                    Button {
                        focusedField = nil
                        store.sendComposerMessage()
                    } label: {
                        Image(systemName: "paperplane.fill")
                            .font(.subheadline.weight(.bold))
                            .frame(width: 38, height: 38)
                    }
                    .foregroundStyle(AgentRoomPalette.canvas)
                    .background(AgentRoomPalette.me, in: Circle())
                    .buttonStyle(.plain)
                    .disabled(store.room == nil)
                    .accessibilityLabel("Send message")
                }
            }
            .padding(.horizontal, 10)
            .padding(.vertical, 7)
            .background(AgentRoomPalette.composerField, in: Capsule())

            HStack(spacing: 0) {
                composerTool(systemName: "at", title: store.shortStickyTargetLabel) {
                    focusedField = .composer
                }
                composerTool(systemName: "textformat", title: "Aa") {}
                composerTool(systemName: "hand.thumbsup", title: "Like") {}
                composerTool(systemName: "photo", title: "Photo") {}
                composerTool(systemName: "sparkles.rectangle.stack", title: "AI") {}
                composerTool(systemName: "face.smiling", title: "Emoji") {}
                composerTool(systemName: "plus.circle", title: "More") {}
            }
            .frame(height: 32)
        }
        .padding(.horizontal, 10)
        .padding(.top, 7)
        .padding(.bottom, focusedField == .composer ? 6 : 8)
        .background(AgentRoomPalette.composerBar)
        .overlay(alignment: .top) {
            Rectangle().fill(AgentRoomPalette.border).frame(height: 1)
        }
    }

    private var mentionTargetMenu: some View {
        Menu {
            Button {
                store.setStickyTarget(nil)
            } label: {
                Label("Broadcast", systemImage: store.stickyTarget == nil ? "checkmark" : "dot.radiowaves.left.and.right")
            }

            ForEach(store.agentParticipants.prefix(12)) { participant in
                Button {
                    store.setStickyTarget(participant.id)
                    focusedField = .composer
                } label: {
                    Label("@\(participant.id)", systemImage: store.stickyTarget == participant.id ? "checkmark" : "cpu")
                }
            }
        } label: {
            Image(systemName: "at.circle")
                .font(.title3.weight(.semibold))
                .frame(width: 32, height: 32)
                .foregroundStyle(store.stickyTarget == nil ? AgentRoomPalette.status : AgentRoomPalette.me)
        }
        .buttonStyle(.plain)
        .accessibilityLabel("Choose default mention target")
    }

    private func composerTool(systemName: String, title: String, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            Image(systemName: systemName)
                .font(.title3.weight(.medium))
                .frame(maxWidth: .infinity)
                .frame(height: 32)
                .contentShape(Rectangle())
        }
        .foregroundStyle(AgentRoomPalette.composerIcon)
        .buttonStyle(.plain)
        .accessibilityLabel(title)
    }

    private func dismissKeyboard() {
        guard focusedField != nil else { return }
        focusedField = nil
        #if os(iOS)
        UIApplication.shared.sendAction(#selector(UIResponder.resignFirstResponder), to: nil, from: nil, for: nil)
        #endif
    }

    private func leaveCurrentRoom(animated: Bool = true) {
        dismissKeyboard()
        if animated {
            withAnimation(.easeOut(duration: 0.22)) {
                store.leaveRoom()
            }
        } else {
            var transaction = Transaction()
            transaction.disablesAnimations = true
            withTransaction(transaction) {
                store.leaveRoom()
            }
        }
        backSwipeOffset = 0
    }

    private func leaveCurrentRoom() {
        leaveCurrentRoom(animated: true)
    }

    private func scrollTimelineToBottom(_ proxy: ScrollViewProxy, bottomAnchorID: String, animated: Bool) {
        DispatchQueue.main.async {
            if animated {
                withAnimation(.easeOut(duration: 0.22)) {
                    proxy.scrollTo(bottomAnchorID, anchor: .bottom)
                }
            } else {
                proxy.scrollTo(bottomAnchorID, anchor: .bottom)
            }
        }
    }
}

private enum AgentRoomFocusField: Hashable {
    case roomID
    case composer
}

private enum MobileTab: String, CaseIterable, Identifiable {
    case rooms
    case activity
    case agents
    case settings

    var id: String { rawValue }

    var title: String {
        switch self {
        case .rooms:
            return "rooms"
        case .activity:
            return "activity"
        case .agents:
            return "agents"
        case .settings:
            return "settings"
        }
    }

    var systemImage: String {
        switch self {
        case .rooms:
            return "number"
        case .activity:
            return "waveform.path.ecg"
        case .agents:
            return "cpu"
        case .settings:
            return "gearshape"
        }
    }
}

private struct TerminalTabBar: View {
    @Binding var selectedTab: MobileTab

    var body: some View {
        HStack(spacing: 6) {
            ForEach(MobileTab.allCases) { tab in
                Button {
                    selectedTab = tab
                } label: {
                    VStack(spacing: 3) {
                        Image(systemName: tab.systemImage)
                            .font(.caption.weight(.bold))
                        Text(tab.title)
                            .font(.system(size: 10, design: .monospaced).weight(.bold))
                            .lineLimit(1)
                            .minimumScaleFactor(0.75)
                    }
                    .frame(maxWidth: .infinity)
                    .frame(height: 46)
                }
                .foregroundStyle(selectedTab == tab ? AgentRoomPalette.canvas : AgentRoomPalette.status)
                .background(selectedTab == tab ? AgentRoomPalette.me : AgentRoomPalette.panel, in: RoundedRectangle(cornerRadius: 6))
                .overlay(
                    RoundedRectangle(cornerRadius: 6)
                        .stroke(selectedTab == tab ? AgentRoomPalette.me.opacity(0.8) : AgentRoomPalette.border, lineWidth: 1)
                )
                .buttonStyle(.plain)
            }
        }
    }
}

private struct MiniFeatureRow: View {
    let icon: String
    let title: String
    let value: String

    var body: some View {
        HStack(spacing: 8) {
            Image(systemName: icon)
                .font(.caption.weight(.bold))
                .foregroundStyle(AgentRoomPalette.me)
                .frame(width: 20)

            Text(title)
                .terminalCaption()
                .foregroundStyle(AgentRoomPalette.text)

            Spacer(minLength: 8)

            Text(value)
                .terminalCaption()
                .foregroundStyle(AgentRoomPalette.dim)
                .lineLimit(1)
                .minimumScaleFactor(0.75)
        }
    }
}

private struct ActivityRow: View {
    let message: AgentRoomMessage

    var body: some View {
        HStack(spacing: 8) {
            Text(message.type.rawValue)
                .terminalCaption()
                .foregroundStyle(message.senderKind.terminalColor)
                .frame(width: 72, alignment: .leading)

            VStack(alignment: .leading, spacing: 2) {
                Text(message.content.isEmpty ? "(empty)" : message.content)
                    .font(.system(.caption, design: .monospaced).weight(.semibold))
                    .foregroundStyle(AgentRoomPalette.text)
                    .lineLimit(1)
                Text("\(message.senderID) · \(message.createdAt.formatted(date: .omitted, time: .shortened))")
                    .terminalCaption()
                    .foregroundStyle(AgentRoomPalette.dim)
                    .lineLimit(1)
            }

            Spacer(minLength: 0)
        }
        .padding(.horizontal, 10)
        .frame(height: 48)
    }
}

private struct ParticipantRow: View {
    let participant: AgentRoomParticipant
    let isSelected: Bool

    var body: some View {
        MentionTargetRow(
            icon: participant.kind == .agent ? "cpu.fill" : "person.fill",
            title: participant.label,
            subtitle: participant.metadata?["capabilities"] ?? participant.kind.rawValue,
            isSelected: isSelected,
            tint: participant.kind.terminalColor
        )
    }
}

private struct MentionTargetRow: View {
    let icon: String
    let title: String
    let subtitle: String
    let isSelected: Bool
    let tint: Color

    var body: some View {
        HStack(spacing: 8) {
            Image(systemName: icon)
                .font(.caption.weight(.bold))
                .foregroundStyle(tint)
                .frame(width: 22)

            VStack(alignment: .leading, spacing: 2) {
                Text(title)
                    .font(.system(.callout, design: .monospaced).weight(.semibold))
                    .foregroundStyle(AgentRoomPalette.text)
                    .lineLimit(1)
                Text(subtitle)
                    .terminalCaption()
                    .foregroundStyle(AgentRoomPalette.dim)
                    .lineLimit(1)
            }

            Spacer()

            if isSelected {
                Image(systemName: "checkmark.circle.fill")
                    .font(.subheadline.weight(.bold))
                    .foregroundStyle(AgentRoomPalette.me)
            } else {
                Text("online")
                    .terminalCaption()
                    .foregroundStyle(tint)
            }
        }
        .padding(.horizontal, 10)
        .frame(height: 50)
        .background(isSelected ? AgentRoomPalette.me.opacity(0.08) : Color.clear)
        .contentShape(Rectangle())
    }
}

private struct CommandJoinPanel: View {
    @Binding var roomDraft: String
    let focus: FocusState<AgentRoomFocusField?>.Binding
    let canJoin: Bool
    let join: () -> Void
    let create: () -> Void

    var body: some View {
        TerminalPanel(spacing: 8) {
            HStack(spacing: 8) {
                Text("join")
                    .terminalCaption()
                    .foregroundStyle(AgentRoomPalette.status)

                Text("room id / new session")
                    .terminalCaption()
                    .foregroundStyle(AgentRoomPalette.dim)

                Spacer()
            }

            HStack(spacing: 8) {
                Text(">")
                    .terminalHeadline()
                    .foregroundStyle(AgentRoomPalette.me)

                TextField("room_id", text: $roomDraft)
                    .agentRoomPlainInput()
                    .focused(focus, equals: .roomID)
                    .textFieldStyle(.plain)
                    .font(.system(.callout, design: .monospaced).weight(.medium))
                    .foregroundStyle(AgentRoomPalette.text)
                    .submitLabel(.join)
                    .onSubmit {
                        join()
                    }

                if !roomDraft.isEmpty {
                    Button {
                        roomDraft = ""
                    } label: {
                        Image(systemName: "xmark.circle.fill")
                            .font(.subheadline.weight(.semibold))
                            .frame(width: 44, height: 44)
                    }
                    .foregroundStyle(AgentRoomPalette.dim)
                    .buttonStyle(.plain)
                    .accessibilityLabel("Clear room ID")
                }

                Button(action: join) {
                    Image(systemName: "arrow.right")
                        .font(.subheadline.weight(.semibold))
                        .frame(width: 44, height: 44)
                }
                .foregroundStyle(AgentRoomPalette.canvas)
                .background(AgentRoomPalette.me, in: RoundedRectangle(cornerRadius: 6))
                .buttonStyle(.plain)
                .disabled(!canJoin)
                .opacity(canJoin ? 1 : 0.42)

                Button(action: create) {
                    Image(systemName: "plus")
                        .font(.subheadline.weight(.semibold))
                        .frame(width: 44, height: 44)
                }
                .foregroundStyle(AgentRoomPalette.ai)
                .background(AgentRoomPalette.ai.opacity(0.14), in: RoundedRectangle(cornerRadius: 6))
                .overlay(
                    RoundedRectangle(cornerRadius: 6)
                        .stroke(AgentRoomPalette.ai.opacity(0.48), lineWidth: 1)
                )
                .buttonStyle(.plain)
            }
            .padding(.horizontal, 8)
            .frame(height: 52)
            .background(AgentRoomPalette.field, in: RoundedRectangle(cornerRadius: 6))
            .overlay(
                RoundedRectangle(cornerRadius: 6)
                    .stroke(AgentRoomPalette.border, lineWidth: 1)
            )
        }
    }
}

private struct TerminalTopBar: View {
    let left: String
    let middle: String
    let right: String
    let leadingIcon: String?
    let trailingIcon: String
    let leadingAction: (() -> Void)?
    let trailingAction: () -> Void

    var body: some View {
        HStack(spacing: 8) {
            if let leadingIcon, let leadingAction {
                BarIcon(systemName: leadingIcon, action: leadingAction)
            }

            HStack(spacing: 5) {
                Text(left)
                    .lineLimit(1)
                    .truncationMode(.middle)
                Text("|")
                    .foregroundStyle(AgentRoomPalette.dim)
                Text(middle)
                    .lineLimit(1)
                    .truncationMode(.tail)
                Text("|")
                    .foregroundStyle(AgentRoomPalette.dim)
                Text(right)
                    .lineLimit(1)
            }
            .font(.system(.caption, design: .monospaced).weight(.semibold))
            .foregroundStyle(AgentRoomPalette.status)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, 9)
            .frame(height: 44)
            .background(AgentRoomPalette.panel, in: RoundedRectangle(cornerRadius: 6))
            .overlay(
                RoundedRectangle(cornerRadius: 6)
                    .stroke(AgentRoomPalette.border, lineWidth: 1)
            )

            BarIcon(systemName: trailingIcon, action: trailingAction)
        }
    }
}

private struct BarIcon: View {
    let systemName: String
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            Image(systemName: systemName)
                .font(.subheadline.weight(.bold))
                .frame(width: 44, height: 44)
        }
        .foregroundStyle(AgentRoomPalette.me)
        .background(AgentRoomPalette.panel, in: RoundedRectangle(cornerRadius: 6))
        .overlay(
            RoundedRectangle(cornerRadius: 6)
                .stroke(AgentRoomPalette.border, lineWidth: 1)
        )
        .buttonStyle(.plain)
    }
}

private struct ComposerIconButton: View {
    let systemName: String
    let tint: Color
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            Image(systemName: systemName)
                .font(.title3.weight(.semibold))
                .frame(width: 34, height: 34)
        }
        .foregroundStyle(tint)
        .buttonStyle(.plain)
    }
}

private struct TerminalPanel<Content: View>: View {
    let spacing: CGFloat
    @ViewBuilder var content: Content

    var body: some View {
        VStack(alignment: .leading, spacing: spacing) {
            content
        }
        .padding(9)
        .background(AgentRoomPalette.panel, in: RoundedRectangle(cornerRadius: 6))
        .overlay(
            RoundedRectangle(cornerRadius: 6)
                .stroke(AgentRoomPalette.border, lineWidth: 1)
        )
    }
}

private struct RoomListRow: View {
    let room: AgentRoomSummary

    var body: some View {
        HStack(spacing: 9) {
            Text("#")
                .terminalHeadline()
                .foregroundStyle(AgentRoomPalette.ai)
                .frame(width: 22)

            VStack(alignment: .leading, spacing: 2) {
                Text(room.title ?? room.id)
                    .font(.system(.callout, design: .monospaced).weight(.semibold))
                    .foregroundStyle(AgentRoomPalette.text)
                    .lineLimit(1)
                    .truncationMode(.middle)

                Text("opened \(room.lastOpenedAt, style: .relative)")
                    .terminalCaption()
                    .foregroundStyle(AgentRoomPalette.dim)
            }

            Spacer()

            Text("open")
                .terminalCaption()
                .foregroundStyle(AgentRoomPalette.me)
        }
        .padding(.horizontal, 10)
        .frame(height: 46)
        .contentShape(Rectangle())
    }
}

private struct EmptyRoomsRow: View {
    let create: () -> Void

    var body: some View {
        HStack(spacing: 9) {
            Text("--")
                .terminalCaption()
                .foregroundStyle(AgentRoomPalette.dim)
                .frame(width: 22)

            VStack(alignment: .leading, spacing: 2) {
                Text("no rooms yet")
                    .font(.system(.callout, design: .monospaced).weight(.semibold))
                    .foregroundStyle(AgentRoomPalette.text)

                Text("create a session to start")
                    .terminalCaption()
                    .foregroundStyle(AgentRoomPalette.dim)
            }

            Spacer()

            Button(action: create) {
                Text("new")
                    .terminalCaption()
                    .foregroundStyle(AgentRoomPalette.canvas)
                    .padding(.horizontal, 10)
                    .frame(minWidth: 48, minHeight: 44)
                    .background(AgentRoomPalette.me, in: RoundedRectangle(cornerRadius: 6))
            }
            .buttonStyle(.plain)
        }
        .padding(.horizontal, 10)
        .frame(height: 52)
    }
}

private struct StatusPill: View {
    let title: String
    let systemImage: String
    let tint: Color

    var body: some View {
        HStack(spacing: 5) {
            Image(systemName: systemImage)
                .font(.caption2.weight(.semibold))
            Text(title)
                .terminalCaption()
                .lineLimit(1)
        }
        .foregroundStyle(tint)
        .padding(.horizontal, 8)
        .frame(height: 24)
        .background(tint.opacity(0.12), in: RoundedRectangle(cornerRadius: 6))
        .overlay(
            RoundedRectangle(cornerRadius: 6)
                .stroke(tint.opacity(0.35), lineWidth: 1)
        )
    }
}

private struct TerminalTag: View {
    let text: String
    let tint: Color

    var body: some View {
        Text(text)
            .terminalCaption()
            .foregroundStyle(tint)
            .lineLimit(1)
            .padding(.horizontal, 8)
            .frame(height: 24)
            .background(tint.opacity(0.1), in: RoundedRectangle(cornerRadius: 6))
            .overlay(
                RoundedRectangle(cornerRadius: 6)
                    .stroke(tint.opacity(0.32), lineWidth: 1)
            )
    }
}

struct TimelineEntry: Identifiable {
    let id: String
    var message: AgentRoomMessage?
    var traces: [AgentRoomMessage]
}

private struct TimelineEntryRow: View {
    let entry: TimelineEntry

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            if let message = entry.message, message.senderKind == .agent {
                traceSummary
                TimelineMessageRow(message: message)
            } else {
                if let message = entry.message {
                    TimelineMessageRow(message: message)
                }
                traceSummary
            }
        }
    }

    @ViewBuilder
    private var traceSummary: some View {
        if !entry.traces.isEmpty {
            TraceStatusSummaryRow(traces: entry.traces)
        }
    }
}

private struct TimelineMessageRow: View {
    let message: AgentRoomMessage

    var body: some View {
        switch message.type {
        case .chat:
            MessageRow(message: message)
        case .trace:
            EmptyView()
        case .command:
            CommandMessageRow(message: message)
        case .commandResult:
            CommandResultMessageRow(message: message)
        case .presence, .system, .control:
            EventMessageRow(message: message)
        }
    }
}

private struct MessageRow: View {
    let message: AgentRoomMessage

    var body: some View {
        HStack(alignment: .top) {
            if isOutgoing {
                Spacer(minLength: 28)
            }

            VStack(alignment: isOutgoing ? .trailing : .leading, spacing: 4) {
                HStack(spacing: 6) {
                    Text(message.senderKind.rawValue)
                        .terminalCaption()
                        .foregroundStyle(kindColor)

                    Text(message.createdAt, style: .time)
                        .terminalCaption()
                        .foregroundStyle(AgentRoomPalette.dim)
                }

                Text(message.content.isEmpty ? "(empty)" : message.content)
                    .font(.system(.callout, design: .monospaced))
                    .foregroundStyle(AgentRoomPalette.text)
                    .textSelection(.enabled)
                    .padding(.horizontal, 9)
                    .padding(.vertical, 8)
                    .background(bubbleColor, in: RoundedRectangle(cornerRadius: 6))
                    .overlay(
                        RoundedRectangle(cornerRadius: 6)
                            .stroke(kindColor.opacity(0.4), lineWidth: 1)
                    )
            }
            .frame(maxWidth: 310, alignment: isOutgoing ? .trailing : .leading)

            if !isOutgoing {
                Spacer(minLength: 28)
            }
        }
    }

    private var isOutgoing: Bool {
        message.senderKind == .user
    }

    private var kindColor: Color {
        message.senderKind.terminalColor
    }

    private var bubbleColor: Color {
        switch message.senderKind {
        case .user:
            return AgentRoomPalette.me.opacity(0.22)
        case .agent:
            return AgentRoomPalette.ai.opacity(0.18)
        case .system:
            return AgentRoomPalette.warning.opacity(0.14)
        }
    }
}

func buildTimelineEntries(_ messages: [AgentRoomMessage]) -> [TimelineEntry] {
    var entries: [TimelineEntry] = []
    var traceGroups: [String: [AgentRoomMessage]] = [:]
    var traceGroupOrder: [String] = []
    var traceGroupEntryIndex: [String: Int] = [:]
    var messageEntryIndex: [String: Int] = [:]

    func appendTrace(_ trace: AgentRoomMessage, key: String) {
        if traceGroups[key] == nil {
            traceGroups[key] = []
            traceGroupOrder.append(key)
        }
        traceGroups[key]?.append(trace)
        if let index = traceGroupEntryIndex[key], entries.indices.contains(index) {
            entries[index].traces = merged(entries[index].traces, traceGroups[key] ?? [])
        }
    }

    func bestTraceGroup(for message: AgentRoomMessage) -> String? {
        guard message.type == .chat, message.senderKind == .agent else { return nil }
        return traceGroupOrder.reversed().first { key in
            guard traceGroupEntryIndex[key] == nil, let traces = traceGroups[key], let first = traces.first else {
                return false
            }
            return first.senderID == message.senderID && (first.targetID ?? "") == (message.targetID ?? "")
        }
    }

    for message in messages {
        if message.type == .trace {
            appendTrace(message, key: traceGroupKey(message))
            continue
        }

        if message.type == .control {
            continue
        }

        let matchedTraceKey = bestTraceGroup(for: message)
        let entryID = matchedTraceKey.map { "entry:\($0):\(message.id)" } ?? message.id
        let entry = TimelineEntry(
            id: entryID,
            message: message,
            traces: matchedTraceKey.flatMap { traceGroups[$0] } ?? []
        )
        entries.append(entry)
        messageEntryIndex[message.id] = entries.count - 1
        if let matchedTraceKey {
            traceGroupEntryIndex[matchedTraceKey] = entries.count - 1
        }
    }

    for key in traceGroupOrder where traceGroupEntryIndex[key] == nil {
        let traces = traceGroups[key] ?? []
        if
            let replyTo = traceReplyTo(key),
            let sourceIndex = messageEntryIndex[replyTo],
            entries.indices.contains(sourceIndex)
        {
            entries[sourceIndex].traces = merged(entries[sourceIndex].traces, traces)
            traceGroupEntryIndex[key] = sourceIndex
            continue
        }

        if
            let fallbackIndex = entries.indices.last,
            entries[fallbackIndex].message?.type == .chat
        {
            entries[fallbackIndex].traces = merged(entries[fallbackIndex].traces, traces)
            traceGroupEntryIndex[key] = fallbackIndex
            continue
        }

        entries.append(TimelineEntry(id: "trace:\(key)", message: nil, traces: traces))
    }

    return entries
}

private func traceReplyTo(_ key: String) -> String? {
    guard key.hasPrefix("reply:") else { return nil }
    return String(key.dropFirst("reply:".count))
}

private func merged(_ lhs: [AgentRoomMessage], _ rhs: [AgentRoomMessage]) -> [AgentRoomMessage] {
    var seen = Set(lhs.map(\.id))
    var output = lhs
    for item in rhs where !seen.contains(item.id) {
        output.append(item)
        seen.insert(item.id)
    }
    return output.sorted { $0.createdAt < $1.createdAt }
}

private func traceGroupKey(_ message: AgentRoomMessage) -> String {
    if let replyTo = message.metadata?["reply_to"], !replyTo.isEmpty {
        return "reply:\(replyTo)"
    }
    if let commandID = message.metadata?["command_id"], !commandID.isEmpty {
        return "command:\(commandID)"
    }
    if let toolUseID = message.metadata?["tool_use_id"], !toolUseID.isEmpty {
        return "tool:\(toolUseID)"
    }
    return "trace:\(message.senderID):\(message.targetID ?? ""):\(message.id)"
}

private struct TraceStatusSummaryRow: View {
    let traces: [AgentRoomMessage]

    var body: some View {
        LogCard(
            icon: icon,
            title: title,
            subtitle: subtitle,
            tint: tint
        ) {
            HStack(spacing: 7) {
                if isRunning {
                    ProgressView()
                        .controlSize(.mini)
                        .tint(tint)
                }

                Text(summary)
                    .font(.system(.caption, design: .monospaced).weight(.semibold))
                    .foregroundStyle(AgentRoomPalette.text)
                    .lineLimit(2)
                    .textSelection(.enabled)

                Spacer(minLength: 0)
            }

            MetadataChipRow(items: chips, tint: tint)
        }
    }

    private var first: AgentRoomMessage {
        traces[0]
    }

    private var latest: AgentRoomMessage {
        traces.last ?? first
    }

    private var phase: String {
        latest.metadata?["phase"] ?? latest.metadata?["event_type"] ?? "trace"
    }

    private var title: String {
        switch phase {
        case "thinking":
            return "thinking"
        case "text":
            return "drafting"
        case "tool_use":
            return "tool use"
        case "tool_result":
            return "tool result"
        case "permission_request":
            return "approval needed"
        case "delegate_exec":
            return "delegate exec"
        case "done":
            return "done"
        case "stopped":
            return "stopped"
        case "error":
            return "error"
        default:
            return phase.replacingOccurrences(of: "_", with: " ")
        }
    }

    private var subtitle: String {
        "\(first.senderID) · \(traces.count) step\(traces.count == 1 ? "" : "s")"
    }

    private var summary: String {
        let tool = latest.metadata?["tool"] ?? ""
        let detail = latest.metadata?["detail"] ?? latest.metadata?["command"] ?? latest.metadata?["input"] ?? ""
        let content = latest.content.trimmingCharacters(in: .whitespacesAndNewlines)

        if !tool.isEmpty {
            return content.isEmpty ? tool : "\(tool): \(short(content, max: 88))"
        }
        if !detail.isEmpty {
            return short(detail, max: 96)
        }
        if !content.isEmpty {
            return short(content, max: 96)
        }
        return title
    }

    private var chips: [String] {
        var values: [String] = []
        let metadata = latest.metadata ?? [:]
        if let provider = metadata["provider"], !provider.isEmpty {
            values.append(provider)
        } else if let provider = first.metadata?["provider"], !provider.isEmpty {
            values.append(provider)
        }
        if let duration = metadata["duration_ms"], !duration.isEmpty {
            values.append(durationLabel(duration))
        }
        if let tool = metadata["tool"], !tool.isEmpty {
            values.append("tool=\(short(tool, max: 22))")
        }
        if let commandID = metadata["command_id"], !commandID.isEmpty {
            values.append("cmd=\(short(commandID, max: 18))")
        }
        return values
    }

    private var isRunning: Bool {
        !["done", "error", "stopped"].contains(phase)
    }

    private var tint: Color {
        switch phase {
        case "done":
            return AgentRoomPalette.me
        case "error", "stopped":
            return AgentRoomPalette.warning
        case "permission_request", "delegate_exec", "tool_use", "tool_result":
            return AgentRoomPalette.ai
        default:
            return AgentRoomPalette.status
        }
    }

    private var icon: String {
        switch phase {
        case "tool_use", "tool_result":
            return "hammer.fill"
        case "permission_request":
            return "lock.shield.fill"
        case "delegate_exec":
            return "terminal.fill"
        case "done":
            return "checkmark.circle.fill"
        case "stopped":
            return "stop.circle.fill"
        case "error":
            return "exclamationmark.triangle.fill"
        default:
            return "brain.head.profile"
        }
    }
}

private struct TraceMessageRow: View {
    let message: AgentRoomMessage

    var body: some View {
        LogCard(
            icon: icon,
            title: title,
            subtitle: "\(message.senderID) · \(message.createdAt.formatted(date: .omitted, time: .shortened))",
            tint: tint
        ) {
            if !message.content.isEmpty {
                Text(message.content)
                    .font(.system(.caption, design: .monospaced).weight(.semibold))
                    .foregroundStyle(AgentRoomPalette.text)
                    .lineLimit(4)
                    .textSelection(.enabled)
            }

            if let codeText {
                CodePreview(text: codeText, tint: tint)
            }

            MetadataChipRow(items: metadataItems, tint: tint)
        }
    }

    private var phase: String {
        message.metadata?["phase"] ?? message.metadata?["event_type"] ?? "trace"
    }

    private var title: String {
        switch phase {
        case "thinking":
            return "thinking"
        case "text":
            return "drafting"
        case "tool_use":
            return "tool use"
        case "tool_result":
            return "tool result"
        case "permission_request":
            return "approval needed"
        case "delegate_exec":
            return "delegate exec"
        case "done":
            return "done"
        case "stopped":
            return "stopped"
        case "error":
            return "error"
        default:
            return phase.replacingOccurrences(of: "_", with: " ")
        }
    }

    private var icon: String {
        switch phase {
        case "thinking", "text":
            return "brain.head.profile"
        case "tool_use", "tool_result":
            return "hammer.fill"
        case "permission_request":
            return "lock.shield.fill"
        case "delegate_exec":
            return "terminal.fill"
        case "done":
            return "checkmark.circle.fill"
        case "stopped":
            return "stop.circle.fill"
        case "error":
            return "exclamationmark.triangle.fill"
        default:
            return "waveform.path.ecg"
        }
    }

    private var tint: Color {
        switch phase {
        case "done":
            return AgentRoomPalette.me
        case "error", "stopped":
            return AgentRoomPalette.warning
        case "permission_request", "delegate_exec", "tool_use", "tool_result":
            return AgentRoomPalette.ai
        default:
            return AgentRoomPalette.status
        }
    }

    private var codeText: String? {
        let metadata = message.metadata ?? [:]
        return metadata["input"] ?? metadata["command"] ?? metadata["detail"] ?? metadata["error"]
    }

    private var metadataItems: [String] {
        compactMetadata(keys: ["tool", "provider", "label", "reply_to", "command_id", "duration_ms", "pattern"])
    }

    private func compactMetadata(keys: [String]) -> [String] {
        let metadata = message.metadata ?? [:]
        return keys.compactMap { key in
            guard let value = metadata[key], !value.isEmpty else { return nil }
            if key == "duration_ms" {
                return "\(durationLabel(value))"
            }
            return "\(key)=\(short(value, max: 28))"
        }
    }
}

private struct CommandMessageRow: View {
    let message: AgentRoomMessage

    var body: some View {
        LogCard(
            icon: "terminal.fill",
            title: "command",
            subtitle: "\(message.senderID) -> \(short(message.targetID ?? "executor", max: 24))",
            tint: AgentRoomPalette.ai
        ) {
            CodePreview(text: message.content, tint: AgentRoomPalette.ai)
            MetadataChipRow(items: metadataItems, tint: AgentRoomPalette.ai)
        }
    }

    private var metadataItems: [String] {
        let metadata = message.metadata ?? [:]
        return ["operation", "cwd", "shell", "timeout_ms"].compactMap { key in
            guard let value = metadata[key], !value.isEmpty else { return nil }
            return "\(key)=\(short(value, max: 30))"
        }
    }
}

private struct CommandResultMessageRow: View {
    let message: AgentRoomMessage

    var body: some View {
        LogCard(
            icon: resultOK ? "checkmark.terminal.fill" : "xmark.octagon.fill",
            title: resultOK ? "command result" : "command failed",
            subtitle: "\(message.senderID) · \(exitLabel)",
            tint: tint
        ) {
            if !message.content.isEmpty {
                CodePreview(text: message.content, tint: tint)
            }
            MetadataChipRow(items: metadataItems, tint: tint)
        }
    }

    private var tint: Color {
        resultOK ? AgentRoomPalette.me : AgentRoomPalette.warning
    }

    private var resultOK: Bool {
        (message.metadata?["exit_code"] ?? "0") == "0" && message.metadata?["timed_out"] != "true"
    }

    private var exitLabel: String {
        if message.metadata?["timed_out"] == "true" {
            return "timeout"
        }
        return "exit \(message.metadata?["exit_code"] ?? "?")"
    }

    private var metadataItems: [String] {
        let metadata = message.metadata ?? [:]
        return ["duration_ms", "command_id", "stdout_truncated", "stderr_truncated", "error_type"].compactMap { key in
            guard let value = metadata[key], !value.isEmpty else { return nil }
            if key == "duration_ms" {
                return durationLabel(value)
            }
            return "\(key)=\(short(value, max: 28))"
        }
    }
}

private struct EventMessageRow: View {
    let message: AgentRoomMessage

    var body: some View {
        LogCard(
            icon: icon,
            title: message.type.rawValue,
            subtitle: "\(message.senderID) · \(message.createdAt.formatted(date: .omitted, time: .shortened))",
            tint: tint
        ) {
            Text(message.content.isEmpty ? "(empty)" : message.content)
                .font(.system(.caption, design: .monospaced).weight(.semibold))
                .foregroundStyle(AgentRoomPalette.text)
                .lineLimit(3)
                .textSelection(.enabled)
        }
    }

    private var icon: String {
        switch message.type {
        case .presence:
            return "person.2.fill"
        case .system:
            return "info.circle.fill"
        case .control:
            return "slider.horizontal.3"
        default:
            return "dot.radiowaves.left.and.right"
        }
    }

    private var tint: Color {
        message.type == .control ? AgentRoomPalette.ai : AgentRoomPalette.status
    }
}

private struct LogCard<Content: View>: View {
    let icon: String
    let title: String
    let subtitle: String
    let tint: Color
    @ViewBuilder var content: Content

    var body: some View {
        HStack(alignment: .top, spacing: 8) {
            Image(systemName: icon)
                .font(.caption.weight(.bold))
                .foregroundStyle(tint)
                .frame(width: 22, height: 22)
                .background(tint.opacity(0.12), in: RoundedRectangle(cornerRadius: 5))

            VStack(alignment: .leading, spacing: 6) {
                HStack(spacing: 6) {
                    Text(title)
                        .font(.system(.caption, design: .monospaced).weight(.bold))
                        .foregroundStyle(tint)
                        .lineLimit(1)

                    Text(subtitle)
                        .terminalCaption()
                        .foregroundStyle(AgentRoomPalette.dim)
                        .lineLimit(1)
                        .truncationMode(.middle)

                    Spacer(minLength: 0)
                }

                content
            }
        }
        .padding(9)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(AgentRoomPalette.field, in: RoundedRectangle(cornerRadius: 6))
        .overlay(
            RoundedRectangle(cornerRadius: 6)
                .stroke(tint.opacity(0.28), lineWidth: 1)
        )
    }
}

private struct CodePreview: View {
    let text: String
    let tint: Color

    var body: some View {
        Text(text.isEmpty ? "(empty)" : text)
            .font(.system(.caption2, design: .monospaced))
            .foregroundStyle(AgentRoomPalette.text)
            .lineLimit(8)
            .textSelection(.enabled)
            .padding(.horizontal, 8)
            .padding(.vertical, 7)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(AgentRoomPalette.canvas.opacity(0.72), in: RoundedRectangle(cornerRadius: 5))
            .overlay(
                RoundedRectangle(cornerRadius: 5)
                    .stroke(tint.opacity(0.24), lineWidth: 1)
            )
    }
}

private struct MetadataChipRow: View {
    let items: [String]
    let tint: Color

    var body: some View {
        if !items.isEmpty {
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 5) {
                    ForEach(items, id: \.self) { item in
                        Text(item)
                            .font(.system(size: 9, design: .monospaced).weight(.semibold))
                            .foregroundStyle(tint)
                            .lineLimit(1)
                            .padding(.horizontal, 6)
                            .frame(height: 20)
                            .background(tint.opacity(0.09), in: RoundedRectangle(cornerRadius: 5))
                            .overlay(
                                RoundedRectangle(cornerRadius: 5)
                                    .stroke(tint.opacity(0.2), lineWidth: 1)
                            )
                    }
                }
            }
        }
    }
}

private func short(_ value: String, max limit: Int) -> String {
    guard value.count > limit, limit > 4 else { return value }
    let head = max(1, limit - 5)
    return "\(value.prefix(head))...\(value.suffix(2))"
}

private func durationLabel(_ raw: String) -> String {
    guard let ms = Int(raw) else { return "duration=\(raw)" }
    if ms < 1_000 {
        return "\(ms)ms"
    }
    let seconds = Double(ms) / 1_000
    return String(format: "%.1fs", seconds)
}

private struct EmptyConversationView: View {
    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("transcript")
                .terminalCaption()
                .foregroundStyle(AgentRoomPalette.status)
            Text("> room ready")
                .font(.system(.headline, design: .monospaced).weight(.bold))
                .foregroundStyle(AgentRoomPalette.text)
            Text("messages will stream here")
                .terminalCaption()
                .foregroundStyle(AgentRoomPalette.dim)
        }
        .padding(14)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
    }
}

private struct DotMatrixBackground: View {
    var body: some View {
        Canvas { context, size in
            let step: CGFloat = 12
            let dot = Path(ellipseIn: CGRect(x: 0, y: 0, width: 1.4, height: 1.4))
            for x in stride(from: CGFloat(0), through: size.width, by: step) {
                for y in stride(from: CGFloat(0), through: size.height, by: step) {
                    context.translateBy(x: x, y: y)
                    context.fill(dot, with: .color(AgentRoomPalette.dot))
                    context.translateBy(x: -x, y: -y)
                }
            }
        }
    }
}

private enum AgentRoomPalette {
    static let canvas = Color(red: 0.039, green: 0.055, blue: 0.047)
    static let panel = Color(red: 0.078, green: 0.11, blue: 0.094)
    static let field = Color(red: 0.052, green: 0.074, blue: 0.063)
    static let composerBar = Color(red: 0.058, green: 0.077, blue: 0.067)
    static let composerField = Color(red: 0.965, green: 0.984, blue: 0.976).opacity(0.94)
    static let composerIcon = Color(red: 0.835, green: 0.918, blue: 0.884)
    static let text = Color(red: 0.847, green: 0.922, blue: 0.894)
    static let dim = Color(red: 0.478, green: 0.604, blue: 0.549)
    static let status = Color(red: 0.561, green: 0.737, blue: 0.674)
    static let me = Color(red: 0.18, green: 0.769, blue: 0.627)
    static let ai = Color(red: 0.29, green: 0.624, blue: 0.847)
    static let warning = Color(red: 0.902, green: 0.435, blue: 0.435)
    static let border = Color(red: 0.561, green: 0.737, blue: 0.674).opacity(0.22)
    static let dot = Color(red: 0.561, green: 0.737, blue: 0.674).opacity(0.055)
}

#Preview {
    AgentRoomRootView()
}

private extension AgentRoomSenderKind {
    var terminalColor: Color {
        switch self {
        case .user:
            return AgentRoomPalette.me
        case .agent:
            return AgentRoomPalette.ai
        case .system:
            return AgentRoomPalette.warning
        }
    }
}

private extension Text {
    func terminalCaption() -> some View {
        self.font(.system(.caption, design: .monospaced).weight(.semibold))
    }

    func terminalHeadline() -> some View {
        self.font(.system(.headline, design: .monospaced).weight(.bold))
    }
}

private extension View {
    @ViewBuilder
    func agentRoomPlainInput() -> some View {
        #if os(iOS)
        self.textInputAutocapitalization(.never)
            .autocorrectionDisabled(true)
            .keyboardType(.asciiCapable)
        #else
        self
        #endif
    }

    @ViewBuilder
    func agentRoomMessageInput() -> some View {
        #if os(iOS)
        self.submitLabel(.send)
        #else
        self
        #endif
    }

    @ViewBuilder
    func scrollDismissesKeyboardIfAvailable() -> some View {
        #if os(iOS)
        self.scrollDismissesKeyboard(.interactively)
        #else
        self
        #endif
    }
}
