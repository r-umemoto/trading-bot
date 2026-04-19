package strategy

type SimpleStrategyFactory struct {
}

func (f *SimpleStrategyFactory) NewStrategy() Strategy {
	return NewSampleStrategy()
}

func init() {
	Register("sample", &SimpleStrategyFactory{})
}
