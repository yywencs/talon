package sandbox

// Manager 负责通过统一工厂创建 sandbox 实例。
type Manager struct {
	factory Factory
}

// NewManager 创建 sandbox 管理入口。
func NewManager(factory Factory) *Manager {
	if factory == nil {
		factory = DockerFactory{}
	}
	return &Manager{factory: factory}
}

// Create 创建一个新的 sandbox 实例。
func (m *Manager) Create(config Config) Sandbox {
	return m.factory.Create(config)
}

// PlaceholderFactory 表示当前阶段保留的占位 sandbox 工厂。
type PlaceholderFactory struct{}

// Create 创建 sandbox 占位实现。
func (PlaceholderFactory) Create(config Config) Sandbox {
	return NewUnimplementedSandbox(config)
}
