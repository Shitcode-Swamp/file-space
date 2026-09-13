import SwiftUI

struct LoginView: View {
    @EnvironmentObject private var appState: AppState

    @State private var username = ""
    @State private var password = ""
    @State private var isRegisterMode = false

    var body: some View {
        VStack(spacing: 16) {
            Text("FileSpace").font(.largeTitle.bold())
            Text(isRegisterMode ? "Create an account" : "Log in").foregroundStyle(.secondary)

            VStack(spacing: 10) {
                TextField("Username", text: $username)
                    .textFieldStyle(.roundedBorder)
                    .disableAutocorrection(true)
                SecureField("Password", text: $password)
                    .textFieldStyle(.roundedBorder)
            }
            .frame(width: 280)

            if let error = appState.errorMessage {
                Text(error).foregroundStyle(.red).font(.callout)
            }

            Button(isRegisterMode ? "Register" : "Log in") {
                submit()
            }
            .keyboardShortcut(.defaultAction)
            .disabled(username.isEmpty || password.isEmpty || appState.isBusy)

            if appState.isBusy {
                ProgressView()
            }

            Button(isRegisterMode ? "Already have an account? Log in" : "Need an account? Register") {
                isRegisterMode.toggle()
                appState.errorMessage = nil
            }
            .buttonStyle(.link)
        }
        .padding(40)
        .frame(minWidth: 420, minHeight: 380)
    }

    private func submit() {
        Task {
            if isRegisterMode {
                await appState.register(username: username, password: password)
            } else {
                await appState.login(username: username, password: password)
            }
        }
    }
}
